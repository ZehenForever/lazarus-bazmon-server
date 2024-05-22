package main

import (
	"crypto/md5"
	"encoding/csv"
	"encoding/hex"
	"flag"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/antchfx/htmlquery"
	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gopkg.in/ini.v1"
)

var config = flag.String("config", "", "Path to config file")
var monitor = flag.String("monitor", "BazMonitor.ini", "INI file for items to monitor (read)")
var searchResults = flag.String("searchResults", "BazMon_SearchResults.csv", "CSV file for item search results (write)")
var monitorResults = flag.String("monitorResults", "BazMon_MonitorResults.csv", "CSV file for item search results (write)")
var logLevel = flag.String("logLevel", "info", "Log level (debug, info, warn, error, fatal, panic)")

var monitorFile string
var searchResultsFile string
var monitorResultsFile string

var searchQueries = make(map[string]string)
var monitorQueries = make(map[string]string)

var searchQueriesMutex = &sync.Mutex{}
var monitorQueriesMutex = &sync.Mutex{}
var monitorPollDelay = 600

var reSearchTerms = regexp.MustCompile(`((?:\w+\|[\w\s\>\<=]+)+)\/?`)

type SearchTerm struct {
	Key string
	Val string
}

func main() {
	flag.Parse()

	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})

	log.Info().Msg("Starting Bazaar Query Server")

	// Check if ini file is provided
	if *config == "" {
		log.Error().Msg("No path to config ini files provided")
		printUsage()
	}
	if *monitor == "" {
		log.Error().Msg("No monitor ini file provided")
		printUsage()
	}
	if *searchResults == "" {
		log.Error().Msg("No search results CSV file provided")
		printUsage()
	}
	if *monitorResults == "" {
		log.Error().Msg("No monitor results CSV file provided")
		printUsage()
	}
	if *logLevel == "" {
		log.Error().Msg("No log level provided")
		printUsage()
	}

	switch *logLevel {
	case "debug":
		log.Logger = log.Level(zerolog.DebugLevel)
	case "info":
		log.Logger = log.Level(zerolog.InfoLevel)
	case "warn":
		log.Logger = log.Level(zerolog.WarnLevel)
	case "error":
		log.Logger = log.Level(zerolog.ErrorLevel)
	case "fatal":
		log.Logger = log.Level(zerolog.FatalLevel)
	case "panic":
		log.Logger = log.Level(zerolog.PanicLevel)
	default:
		log.Error().Msg("Invalid log level provided")
		printUsage()
	}
	log.Info().Msgf("Using log level: %s", *logLevel)

	// Set the monitor and results file paths
	monitorFile = fmt.Sprintf("%s\\%s", *config, *monitor)
	searchResultsFile = fmt.Sprintf("%s\\%s", *config, *searchResults)
	monitorResultsFile = fmt.Sprintf("%s\\%s", *config, *monitorResults)

	// Provide some info about the files we are using
	log.Info().Msgf("Using monitor file: %s", monitorFile)
	log.Info().Msgf("Using search results file: %s", searchResultsFile)
	log.Info().Msgf("Using monitor results file: %s", monitorResultsFile)

	// Clean out the CSV files
	cleanSearchCSV()
	cleanMonitorCSV()

	// Write the headers to the CSV files
	writeSearchCSVHeader()
	writeMonitorCSVHeader()

	// Process the monitor file for the first time
	processMonitorFile()

	// Watch the monitor file for changes
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal().Msgf("Error creating file watcher for %s: %+v", monitorFile, err)
	}
	defer watcher.Close()

	// Watch the monitor file for changes
	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				log.Debug().Msgf("File event: %+v", event)
				if event.Op&fsnotify.Write == fsnotify.Write {
					//TODO: On Windows10, the file watcher triggers twice for each file change
					log.Info().Msgf("File modified: %s", event.Name)
					processMonitorFile()
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Error().Msgf("File watcher error: %+v", err)
			}
		}
	}()

	// Watch the monitor file for changes
	err = watcher.Add(monitorFile)
	if err != nil {
		log.Fatal().Msgf("Error watching file: %+v", err)
	}

	// Run a background monitor that updates periodically per the config file
	go func() {
		for {
			processMonitorItems()
			time.Sleep(time.Duration(monitorPollDelay) * time.Second)
		}
	}()

	// Block until Interrupt or Kill signal is received
	c := make(chan os.Signal, 1)
	<-c

	log.Info().Msg("Stopping Bazaar Query Server")

}

func processMonitorFile() {
	// Read ini file
	cfg, err := ini.Load(monitorFile)
	if err != nil {
		log.Fatal().Msgf("Fail to read file: %v", err)
	}
	log.Debug().Msgf("Read ini file: %s", monitorFile)

	// Get the monitor poll delay key from the 'General' section
	generalSection, err := cfg.GetSection("General")
	if err != nil {
		log.Info().Msgf("General section does not exist or is empty: %+v", err)
		return
	}
	pollDelay, err := generalSection.GetKey("Monitor Server Poll (seconds)")
	if err != nil {
		log.Warn().Msgf("Monitor Poll Delay setting does not exist or is empty: %+v", err)
	} else {
		// Convert the pollDelay to an int
		monitorPollDelay, err = pollDelay.Int()
		if err != nil {
			log.Info().Msgf("Monitor Poll Delay setting is not an integer: %+v", err)
			return
		}
		if monitorPollDelay < 60 {
			log.Warn().Msgf("Monitor Poll Delay cannot be less than 60 seconds. Setting to 60 seconds")
			monitorPollDelay = 60
		}
	}
	log.Debug().Msgf("Monitor Poll Delay set to %d seconds", monitorPollDelay)

	// Get monitor items from ini file
	monitorSection, err := cfg.GetSection("Monitor")
	if err != nil {
		log.Info().Msgf("Queries section does not exist or is empty: %+v", err)
		return
	}
	log.Debug().Msgf("Registering %d monitor items", len(monitorSection.Keys()))

	// Empty the monitor items queries and add in the current items
	deleteMonitorQueries()
	for _, key := range monitorSection.Keys() {
		updateMonitorQuery(key.Name(), key.Value())
	}

	// Get queries from ini file
	searchSection, err := cfg.GetSection("Queries")
	if err != nil {
		log.Info().Msgf("Queries section does not exist or is empty: %+v", err)
		return
	}
	log.Debug().Msgf("Processing %d search queries", len(searchSection.Keys()))

	// Process the search queries
	processSearchQueries(searchSection.Keys())

}

// processSearchQueries handles the ini 'Queries' section and queries the Bazaar for each item
func processSearchQueries(keys []*ini.Key) {
	for _, key := range keys {
		var searchTerms []SearchTerm

		// If we've already queried this ID, don't do another
		if getSearchQuery(key.Name()) != "" {
			log.Debug().Str("queryID", key.Name()).Msg("Duplicate query. Skipping.")
			continue
		}

		// Update the queries map with the queryID and search terms
		// This will help us from making duplicate queries
		updateSearchQueries(key.Name(), key.Value())

		// Parse out all the search terms from the string
		// Example: "7ioryb7mjb3jz90m=/Name|Item name/Stat|ac/Class|4096/Race|32/Slot|131072"
		var re = reSearchTerms.FindAllStringSubmatch(key.Value(), -1)
		for _, term := range re {
			log.Debug().Str("queryID", key.Name()).Msgf("Search term '%s'", term[1])
			terms := strings.Split(term[1], "|")
			searchTerms = append(searchTerms, SearchTerm{Key: terms[0], Val: terms[1]})
		}
		log.Debug().Str("queryID", key.Name()).Msgf("Search terms: %+v", searchTerms)

		// Query the Bazaar for the item
		rows, err := queryBazaar(key.Name(), searchTerms)
		if err != nil {
			log.Error().Str("queryID", key.Name()).Msgf("Error querying Bazaar: %+v", err)
			continue
		}

		// Write the rows to our search results CSV file
		writeSearchCSV(rows)
		log.Info().Str("queryID", key.Name()).Msgf("Wrote %d rows to search results CSV", len(rows))
	}
}

// processMonitorItems handles the ini 'Monitor' section and queries the Bazaar for each item
func processMonitorItems() {

	monitorQueriesMutex.Lock()
	defer monitorQueriesMutex.Unlock()

	// Iterate over the monitor items and build the search terms
	for key, val := range monitorQueries {
		var re = reSearchTerms.FindAllStringSubmatch(val, -1)

		var searchTerms []SearchTerm
		searchTerms = append(searchTerms, SearchTerm{Key: "Name", Val: key})

		// Parse out all the search terms from the string
		var searchTerm SearchTerm
		for _, term := range re {
			terms := strings.Split(term[1], "|")
			log.Debug().Str("itemName", key).Msgf("Monitor search term '%s'", terms[1])
			if terms[0] == "Price" {
				searchTerm.Val = terms[1]
			} else if terms[0] == "Compare" {
				switch terms[1] {
				case "<":
					searchTerm.Key = "PriceMax"
				case "<=":
					searchTerm.Key = "PriceMax"
				case ">":
					searchTerm.Key = "PriceMin"
				case ">=":
					searchTerm.Key = "PriceMin"
				default:
					log.Warn().Str("itemName", key).Msgf("Unknown compare operator: %s", terms[1])
					continue
				}
			}
			searchTerms = append(searchTerms, searchTerm)
		}

		// Generate a queryID for the monitor item by MD5 hashing the name
		var hash = md5.Sum([]byte(key))
		var hexHash = hex.EncodeToString(hash[:])

		// Delete any items in the CSV that match the queryID (name hash), invalidating previous results
		deleteFromMonitorCSV(hexHash)

		// Query the Bazaar for the item
		rows, err := queryBazaar(hexHash, searchTerms)
		if err != nil {
			log.Error().Str("queryID", hexHash).Msgf("Error querying Bazaar: %+v", err)
			continue
		}

		// Write the rows to our monitor results CSV file
		writeMonitorCSV(rows)
		log.Info().Str("queryID", hexHash).Msgf("Wrote %d rows to monitor results CSV", len(rows))

		// Pause for a bit before querying the next item
		time.Sleep(time.Duration(3) * time.Second)
	}
}

// queryBazaar queries the Bazaar for the item and writes the results to the CSV file
// func queryBazaar(queryID, itemName string) {
func queryBazaar(queryID string, searchTerms []SearchTerm) ([][]string, error) {
	log.Debug().Str("queryID", queryID).Msgf("Querying Bazaar for item '%+v'", searchTerms)

	// CSV rows to store the search results
	var rows [][]string

	// Build our Bazaar web site search URL
	var url = buildURL(searchTerms)
	log.Debug().Str("queryID", queryID).Msgf("Query URL: %s", url)

	// Fetch the Bazaar web site search URL
	doc, err := htmlquery.LoadURL(url)
	if err != nil {
		log.Error().Str("queryID", queryID).Msgf("Error loading URL: %+v", err)
		return rows, err
	}

	// Query the HTML for the search result table rows
	list, err := htmlquery.QueryAll(doc, "//table[@class='CB_Table CB_Highlight_Rows']//tr")
	if err != nil {
		log.Error().Str("queryID", queryID).Msgf("Error querying HTTP response body HTML: %+v", err)
		return rows, err
	}

	// Iterate over the search result table rows and build a slice of rows that will get stored to CSV
	for _, n := range list {

		// Get each td from the list
		tds, err := htmlquery.QueryAll(n, "/td")
		if err != nil {
			log.Error().Str("queryID", queryID).Msgf("Error querying HTTP response body HTML: %+v", err)
			fmt.Println("Error querying HTML doc:", err)
			return rows, err
		}

		// Initialize our results slice with the queryID
		var results = []string{queryID}

		// Get the text from each td and append it to our results
		for _, td := range tds {
			results = append(results, strings.TrimSpace(htmlquery.InnerText(td)))
		}

		// Some html tables are empty rows, so we need to skip those
		if len(results) > 1 {
			rows = append(rows, results)
		}
	}
	log.Debug().Str("queryID", queryID).Msgf("Bazaar search found %d results: %+v", len(rows), rows)

	return rows, nil
}

// buildURL takes a slice of search terms and builds a URL for the Bazaar search
func buildURL(searchTerms []SearchTerm) string {
	var lazUrl = "https://www.lazaruseq.com/Magelo/index.php?page=bazaar"
	for _, term := range searchTerms {
		switch {
		case term.Key == "Name":
			lazUrl += "&item="
		case term.Key == "Class":
			lazUrl += "&class="
		case term.Key == "Race":
			lazUrl += "&race="
		case term.Key == "Stat":
			lazUrl += "&stat="
		case term.Key == "Slot":
			lazUrl += "&slot="
		case term.Key == "Aug":
			lazUrl += "&aug_type="
		case term.Key == "Type":
			lazUrl += "&type="
		case term.Key == "PriceMin":
			lazUrl += "&pricemin="
		case term.Key == "PriceMax":
			lazUrl += "&pricemax="
		case term.Key == "Direction":
			lazUrl += "&direction="
		default:
			continue
		}
		lazUrl += url.QueryEscape(term.Val)
	}
	return lazUrl
}

// cleanSearchCSV opens the CSV file and removes all rows
func cleanSearchCSV() {
	file, err := os.OpenFile(searchResultsFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		log.Fatal().Msgf("Error opening search results file for writing: %+v", err)
	}
	defer file.Close()
	log.Debug().Msg("Cleaned search CSV file, removing all rows")
}

// cleanMonitorCSV opens the CSV file and removes all rows
func cleanMonitorCSV() {
	file, err := os.OpenFile(monitorResultsFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		log.Fatal().Msgf("Error opening monitor results file for writing: %+v", err)
	}
	defer file.Close()
	log.Debug().Msg("Cleaned monitor CSV file, removing all rows")
}

// writeSearchCSVHeader opens the CSV file and writes the header to it
func writeSearchCSVHeader() {

	// Open the file for writing
	file, err := os.OpenFile(searchResultsFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		log.Fatal().Msgf("Error opening search results file for writing: +%v", err)
	}
	defer file.Close()

	// Initialize the CSV writer
	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Write the header
	err = writer.Write([]string{"QueryID", "Item", "Price", "Seller"})
	if err != nil {
		log.Fatal().Msgf("Error writing search results header: +%v", err)
	}
	log.Debug().Msg("Updated search results CSV file with header row")
}

// writeMonitorCSVHeader opens the CSV file and writes the header to it
func writeMonitorCSVHeader() {

	// Open the file for writing
	file, err := os.OpenFile(monitorResultsFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		log.Fatal().Msgf("Error opening monitor results file for writing: +%v", err)
	}
	defer file.Close()

	// Initialize the CSV writer
	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Write the header
	err = writer.Write([]string{"QueryID", "Item", "Price", "Seller"})
	if err != nil {
		log.Fatal().Msgf("Error writing monitor results header: +%v", err)
	}
	log.Debug().Msg("Updated monitor results CSV file with header row")
}

// writeSearchCSV opens the CSV file and writes the data to it
func writeSearchCSV(dataRows [][]string) {

	// Open the file for writing
	file, err := os.OpenFile(searchResultsFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		log.Fatal().Msgf("Error opening search results file for writing: %+v", err)
	}
	defer file.Close()

	// Initialize the CSV writer
	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Iterate through our data and write out those rows
	for _, row := range dataRows {
		err = writer.Write(row)
		if err != nil {
			log.Fatal().Msgf("Error writing search results row: %+v", err)
		}
	}
}

// writeMonitorCSV opens the CSV file and writes the data to it
func writeMonitorCSV(dataRows [][]string) {

	// Open the file for appending
	file, err := os.OpenFile(monitorResultsFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		log.Fatal().Msgf("Error opening monitor results file for writing: %+v", err)
	}
	defer file.Close()

	// Initialize the CSV writer
	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Iterate through our data and write out those rows
	for _, row := range dataRows {
		err = writer.Write(row)
		if err != nil {
			log.Fatal().Msgf("Error writing monitor results row: %+v", err)
		}
	}
}

// deleteFromMonitorCSV opens the CSV file and removes the row with the queryID
func deleteFromMonitorCSV(queryID string) {
	// Open the file for reading and writing
	file, err := os.OpenFile(monitorResultsFile, os.O_RDWR, 0644)
	if err != nil {
		log.Fatal().Msgf("Error opening monitor results file for writing: %+v", err)
	}
	defer file.Close()

	// Read the file into a slice of slices
	reader := csv.NewReader(file)
	rows, err := reader.ReadAll()
	if err != nil {
		log.Fatal().Msgf("Error reading monitor results file: %+v", err)
	}

	// Open the file for writing
	file, err = os.OpenFile(monitorResultsFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		log.Fatal().Msgf("Error opening monitor results file for writing: %+v", err)
	}
	defer file.Close()

	// Initialize the CSV writer
	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Iterate through our data and write out those rows
	for _, row := range rows {
		if row[0] != queryID {
			err = writer.Write(row)
			if err != nil {
				log.Fatal().Msgf("Error writing monitor results row: %+v", err)
			}
		}
	}
}

// getSearchQuery returns the cached search query for the queryID
func getSearchQuery(queryID string) string {
	searchQueriesMutex.Lock()
	defer searchQueriesMutex.Unlock()
	if result, ok := searchQueries[queryID]; ok {
		return result
	}
	return ""
}

// updateSearchQueries updates the searchQueries map with the queryID and search terms
func updateSearchQueries(queryID, search string) {
	searchQueriesMutex.Lock()
	defer searchQueriesMutex.Unlock()
	searchQueries[queryID] = search
}

// updateMonitorQuery updates the monitorQueries map with the queryID and search terms
func updateMonitorQuery(queryID, search string) {
	monitorQueriesMutex.Lock()
	defer monitorQueriesMutex.Unlock()
	monitorQueries[queryID] = search
}

// deleteSearchQueries clears the searchQueries map
func deleteMonitorQueries() {
	monitorQueriesMutex.Lock()
	defer monitorQueriesMutex.Unlock()
	monitorQueries = make(map[string]string)
}

// printUsage prints the usage of the program
func printUsage() {
	fmt.Println("Usage: main -config <path to config files> -monitor <monitor ini file> [-searchResults <csv file>] [-monitorResults <csv file>] [-logLevel <log level>]")
	fmt.Println("   ex: main -config 'C:\\E3NextAndMQNext\\config' -monitor BazMonitor.ini [-searchResults BazMon_SearchResults.csv] [-monitorResults BazMon_MonitorResults.csv] [-logLevel debug]")
	os.Exit(1)
}
