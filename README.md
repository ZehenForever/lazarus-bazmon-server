# lazarus-bazmon-server
A Bazaar search tool for the Lazarus EQ Emulator server.  

## Motivation
The EQ Bazaar interface does not work on Lazarus, so people use the excellent hosted Magelo web site (https://www.lazaruseq.com/Magelo/index.php?page=bazaar) for the Lazarus server.

We are now able to query the Bazaar in game.

## How it works
This is achieved by running two scripts:

1. **Front end**: A normal Lua script ([found here](https://github.com/ZehenForever/lazarus-bazmon-lua)) that is run by MQ's `/lua run <script>` functionality.  It presents the UI and writes its data to a standard .ini file just like any other plugin.  It reads query result data from a separate CSV file populated by the backend server.
1. **Back end**: A standard Go program (this repo) that watches for changes in the Lua .ini file and uses that to essentially proxy queries to the Lazarus Magelo Bazaar search page.  It writes query results to a CSV file that can be in turn read by the Lua script to show those search results in game.

## Usage

### Set up the front end
Setting up and using the the front end Lua script can be found in [its repository](https://www.lazaruseq.com/Magelo/index.php?page=bazaar).

### Set up the backend (this)

1. Download the prebuilt binary from Github (here) or clone the repo and run it via the command line:
    ```shell
    cd C:\YourReposPath\
    git clone git@github.com:ZehenForever/lazarus-bazmon-server.git
    ```
1. Run the main program with a CLI flag that tells it where the MQ/E3 `conf` directory is located.  This must match how you installed your Lua script.  The defaults should work out of the box.
    ```shell
    go run main.go -config '/c/E3NextAndMQNext/config'
    ```
    And optionally with more verbose output, if desired:
    ```shell
    go run main.go -config '/c/E3NextAndMQNext/config' -logLevel debug
    ```

When it first starts up, it should provide some output depending on your log level:

```
2024-03-18T12:44:13-06:00 INF Starting Bazaar Query Server
2024-03-18T12:44:13-06:00 DBG Using log level: debug
2024-03-18T12:44:13-06:00 INF Using monitor file: C:/E3NextAndMQNext/config/BazMonitor.ini
2024-03-18T12:44:13-06:00 INF Using results file: C:/E3NextAndMQNext/config/BazResults.csv
```
And then when the Lua script is run on the client and you click the "Search" button in the UI, that will write to the `BazMonitor.ini` file, and this script will see that change on the file system, read that search request, and query the Lazarus Magelo Bazaar web app, and then write its results to the `BazResults.csv` file.

```
2024-03-18T12:44:26-06:00 DBG File event: WRITE         "C:\\E3NextAndMQNext\\config\\BazMonitor.ini"
2024-03-18T12:44:26-06:00 INF File modified: C:\E3NextAndMQNext\config\BazMonitor.ini
2024-03-18T12:44:26-06:00 DBG Read ini file: C:/E3NextAndMQNext/config/BazMonitor.ini
2024-03-18T12:44:26-06:00 DBG Read 5 queries
2024-03-18T12:44:26-06:00 DBG Cleaned CSV file, removing all rows
2024-03-18T12:44:26-06:00 DBG Updated CSV file with header row
2024-03-18T12:44:26-06:00 INF Running query for 'Fabled Earthshaker' queryID=wrugttwq2wajadhk
2024-03-18T12:44:26-06:00 DBG Querying Bazaar for item 'Fabled Earthshaker' queryID=wrugttwq2wajadhk
2024-03-18T12:44:26-06:00 DBG Query URL: https://www.lazaruseq.com/Magelo/index.php?page=bazaar&item=Fabled+Earthshaker queryID=wrugttwq2wajadhk
2024-03-18T12:44:26-06:00 DBG Writing 2 rows to CSV: [[wrugttwq2wajadhk Fabled Earthshaker 70,000 Baelish] [wrugttwq2wajadhk Fabled Earthshaker 75,000 Coinbuddy]] queryID=wrugttwq2wajadhk
2024-03-18T12:44:26-06:00 INF Wrote 2 rows to CSV queryID=wrugttwq2wajadhk
```

The Lua script in game will poll the CSV file until a configured timeout, and if it finds data matching its query ID, then it will renders those results as a visual table in game.

