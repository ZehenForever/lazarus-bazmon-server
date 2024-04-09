package main

import (
	"encoding/csv"
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
var results = flag.String("results", "BazResults.csv", "INI file for item search results (write)")
var logLevel = flag.String("logLevel", "info", "Log level (debug, info, warn, error, fatal, panic)")

var monitorFile string
var resultsFile string

var queries = make(map[string]string)
var queriesMutex = &sync.Mutex{}

var reSearchTerms = regexp.MustCompile(`((?:\w+\|[\w\s]+)+)\/?`)

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
	if *results == "" {
		log.Error().Msg("No results ini file provided")
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
	resultsFile = fmt.Sprintf("%s\\%s", *config, *results)

	// Provide some info about the files we are using
	log.Info().Msgf("Using monitor file: %s", monitorFile)
	log.Info().Msgf("Using results file: %s", resultsFile)

	// Clean out the results CSV file
	cleanCSV()

	// Write the header to the results CSV file
	writeCSVHeader()

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

	// Get queries from ini file
	section, err := cfg.GetSection("Queries")
	if err != nil {
		log.Info().Msgf("Queries section does not exist or is empty: %+v", err)
		return
	}
	log.Debug().Msgf("Read %d queries", len(section.Keys()))

	log.Debug().Msgf("Processing %d queries", len(section.Keys()))

	// Iterate over key/vals in our ini 'Queries' section
	for _, key := range section.Keys() {
		var searchTerms []SearchTerm

		// If we've already queried this ID, don't do another
		if getQuery(key.Name()) != "" {
			log.Debug().Str("queryID", key.Name()).Msg("Duplicate query. Skipping.")
			continue
		}

		// Update the queries map with the queryID and search terms
		// This will help us from making duplicate queries
		updateQueries(key.Name(), key.Value())

		// Parse out all the search terms from the string
		// Example: "7ioryb7mjb3jz90m=/Name|Item name/Stat|ac/Class|4096/Race|32/Slot|131072"
		var re = reSearchTerms.FindAllStringSubmatch(key.Value(), -1)
		for _, term := range re {
			log.Debug().Str("queryID", key.Name()).Msgf("Search term '%s'", term[1])
			terms := strings.Split(term[1], "|")
			searchTerms = append(searchTerms, SearchTerm{Key: terms[0], Val: terms[1]})
		}
		log.Debug().Str("queryID", key.Name()).Msgf("Search terms: %+v", searchTerms)
		queryBazaar(key.Name(), searchTerms)

		//var itemName = reItemName.FindStringSubmatch(key.Value())[1]
		//log.Info().Str("queryID", key.Name()).Msgf("Running query for '%s'", itemName)
		// Query the Bazaar for each item
		//queryBazaar(key.Name(), itemName)
		//time.Sleep(1 * time.Second)
	}
}

// queryBazaar queries the Bazaar for the item and writes the results to the CSV file
// func queryBazaar(queryID, itemName string) {
func queryBazaar(queryID string, searchTerms []SearchTerm) {
	log.Debug().Str("queryID", queryID).Msgf("Querying Bazaar for item '%+v'", searchTerms)

	// Build our Bazaar web site search URL
	var url = buildURL(searchTerms)
	log.Debug().Str("queryID", queryID).Msgf("Query URL: %s", url)

	// Fetch the Bazaar web site search URL
	doc, err := htmlquery.LoadURL(url)
	if err != nil {
		log.Error().Str("queryID", queryID).Msgf("Error loading URL: %+v", err)
		return
	}

	// Query the HTML for the search result table rows
	list, err := htmlquery.QueryAll(doc, "//table[@class='CB_Table CB_Highlight_Rows']//tr")
	if err != nil {
		log.Error().Str("queryID", queryID).Msgf("Error querying HTTP response body HTML: %+v", err)
		return
	}

	// Iterate over the search result table rows and build a slice of rows that will get stored to CSV
	var rows [][]string
	for _, n := range list {
		// Get each td from the list
		tds, err := htmlquery.QueryAll(n, "/td")
		if err != nil {
			log.Error().Str("queryID", queryID).Msgf("Error querying HTTP response body HTML: %+v", err)
			fmt.Println("Error querying HTML doc:", err)
			return
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
	log.Debug().Str("queryID", queryID).Msgf("Writing %d rows to CSV: %+v", len(rows), rows)

	// Write the rows to the CSV file
	writeCSV(rows)
	log.Info().Str("queryID", queryID).Msgf("Wrote %d rows to CSV", len(rows))
}

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
		default:
			continue
		}
		lazUrl += url.QueryEscape(term.Val)
	}
	return lazUrl
}

// cleanCSV opens the CSV file and removes all rows
func cleanCSV() {
	file, err := os.OpenFile(resultsFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		log.Fatal().Msgf("Error opening Results file for writing: %+v", err)
	}
	defer file.Close()
	log.Debug().Msg("Cleaned CSV file, removing all rows")
}

// writeCSVHeader opens the CSV file and writes the header to it
func writeCSVHeader() {

	// Open the file for writing
	file, err := os.OpenFile(resultsFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		log.Fatal().Msgf("Error opening Results file for writing: +%v", err)
	}
	defer file.Close()

	// Initialize the CSV writer
	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Write the header
	err = writer.Write([]string{"QueryID", "Item", "Price", "Seller"})
	if err != nil {
		log.Fatal().Msgf("Error writing header: +%v", err)
	}
	log.Debug().Msg("Updated CSV file with header row")
}

// writeCSV opens the CSV file and writes the data to it
func writeCSV(dataRows [][]string) {

	// Open the file for writing
	file, err := os.OpenFile(resultsFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		log.Fatal().Msgf("Error opening Results file for writing: %+v", err)
	}
	defer file.Close()

	// Initialize the CSV writer
	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Iterate through our data and write out those rows
	for _, row := range dataRows {
		err = writer.Write(row)
		if err != nil {
			log.Fatal().Msgf("Error writing row: %+v", err)
		}
	}
}

func getQuery(queryID string) string {
	queriesMutex.Lock()
	defer queriesMutex.Unlock()
	if result, ok := queries[queryID]; ok {
		return result
	}
	return ""
}

func updateQueries(queryID, search string) {
	queriesMutex.Lock()
	defer queriesMutex.Unlock()
	queries[queryID] = search
}

func printUsage() {
	fmt.Println("Usage: main -config <path to config files> -monitor <monitor ini file> -results <results ini file>")
	fmt.Println("   ex: main -config 'C:\\E3NextAndMQNext\\config' -monitor BazMonitor.ini -results BazResults.ini")
	os.Exit(1)
}
