package common

import (
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

var (
	// Map of all historical prices. Date as "yyyy-mm-dd" to price in cents
	historicalPrices map[string]float64 = make(map[string]float64)

	// The latest price
	latestPrice float64 = -1

	// Mutex to control both historical and latest price
	pricesRwMutex sync.RWMutex

	// Full path of the persistence file
	pricesFileName string
)

func fetchCoinCapPrice() (float64, error) {
	resp, err := http.Get("https://api.coincap.io/v2/rates/zcash")
	if err != nil {
		return -1, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return -1, err
	}

	var priceJSON map[string]interface{}
	json.Unmarshal(body, &priceJSON)

	data, ok := priceJSON["data"].(map[string]interface{})
	if !ok {
		return -1, errors.New("API error. Couldn't find 'data'")
	}

	rateUSD, ok := data["rateUsd"].(string)
	if !ok {
		return -1, errors.New("API error. Couldn't find 'rateUsd'")
	}

	price, err := strconv.ParseFloat(rateUSD, 64)
	return price, err
}

func fetchHistoricalCoingeckoPrice(ts *time.Time) (float64, error) {
	dt := ts.Format("02-01-2006") // dd-mm-yyyy
	url := fmt.Sprintf("https://api.coingecko.com/api/v3/coins/zcash/history?date=%s?id=zcash", dt)

	resp, err := http.Get(url)
	if err != nil {
		return -1, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return -1, err
	}

	var priceJSON map[string]interface{}
	json.Unmarshal(body, &priceJSON)
	market_price, ok := priceJSON["market_data"].(map[string]interface{})
	if !ok {
		return -1, errors.New("API error. Couldn't find 'market_data'")
	}

	cur_price, ok := market_price["current_price"].(map[string]float64)
	if !ok {
		return -1, errors.New("API error. Couldn't find 'current_price'")
	}

	price, ok := cur_price["usd"]
	if !ok {
		return -1, errors.New("API error. Couldn't find 'usd'")
	}

	return price, err
}

// fetchPriceFromWebAPI will fetch prices from multiple places, discard outliers and return the
// concensus price
func fetchPriceFromWebAPI() (float64, error) {
	price, err := fetchCoinCapPrice()

	return price, err
}

func readHistoricalPricesFile() (map[string]float64, error) {
	f, err := os.Open(pricesFileName)
	if err != nil {
		Log.Errorf("Couldn't open file %s for writing: %v", pricesFileName, err)
		return nil, err
	}
	defer f.Close()

	j := gob.NewDecoder(f)
	var prices map[string]float64
	err = j.Decode(&prices)
	if err != nil {
		Log.Errorf("Couldn't decode historical prices: %v", err)
		return nil, err
	}

	Log.WithFields(logrus.Fields{
		"method":  "HistoricalPrice",
		"action":  "Read historical prices file",
		"records": len(prices),
	}).Info("Service")
	return prices, nil
}

func writeHistoricalPricesMap() {
	// Serialize the map to disk.
	f, err := os.OpenFile(pricesFileName, os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		Log.Errorf("Couldn't open file %s for writing: %v", pricesFileName, err)
		return
	}
	defer f.Close()

	j := gob.NewEncoder(f)

	{
		// Read lock
		pricesRwMutex.RLock()
		defer pricesRwMutex.RUnlock()

		err = j.Encode(historicalPrices)
		if err != nil {
			Log.Errorf("Couldn't encode historical prices: %v", err)
			return
		}
	}

	Log.WithFields(logrus.Fields{
		"method": "HistoricalPrice",
		"action": "Wrote historical prices file",
	}).Info("Service")
}

func GetCurrentPrice() float64 {
	// Read lock
	pricesRwMutex.RLock()
	defer pricesRwMutex.RUnlock()

	return latestPrice
}

func writeLatestPrice(price float64) {
	// Write lock
	pricesRwMutex.Lock()
	defer pricesRwMutex.Unlock()

	latestPrice = price
}

func GetHistoricalPrice(ts *time.Time) (float64, *time.Time, error) {
	dt := ts.Format("2006-01-02")
	canonicalTime, err := time.Parse("2006-01-02", dt)
	if err != nil {
		return -1, nil, err
	}

	{
		// Read lock
		pricesRwMutex.RLock()
		defer pricesRwMutex.RUnlock()
		if val, ok := historicalPrices[dt]; ok {
			return val, &canonicalTime, nil
		}
	}

	// Fetch price from web API
	price, err := fetchHistoricalCoingeckoPrice(ts)
	if err != nil {
		Log.Errorf("Couldn't read historical prices from Coingecko: %v", err)
		return -1, nil, err
	}

	go addHistoricalPrice(price, ts)

	return price, &canonicalTime, err
}

func addHistoricalPrice(price float64, ts *time.Time) {
	dt := ts.Format("2006-01-02")

	// Write lock
	pricesRwMutex.Lock()
	defer pricesRwMutex.Unlock()

	if _, ok := historicalPrices[dt]; !ok {
		historicalPrices[dt] = price

		go Log.WithFields(logrus.Fields{
			"method": "HistoricalPrice",
			"action": "Add",
			"date":   dt,
			"price":  price,
		}).Info("Service")
		go writeHistoricalPricesMap()
	}
}

// StartPriceFetcher starts a new thread that will fetch historical and current prices
func StartPriceFetcher(dbPath string, chainName string) {
	// Set the prices file name
	pricesFileName = filepath.Join(dbPath, chainName, "prices")

	// Read the historical prices if available
	if prices, err := readHistoricalPricesFile(); err != nil {
		Log.Errorf("Couldn't read historical prices, starting with empty map")
	} else {
		// Write lock
		pricesRwMutex.Lock()
		defer pricesRwMutex.Unlock()

		historicalPrices = prices
	}

	// Fetch the current price every hour
	go func() {
		for {
			price, err := fetchPriceFromWebAPI()
			if err != nil {
				Log.Errorf("Error getting coincap.io price: %v", err)
			} else {
				Log.WithFields(logrus.Fields{
					"method": "CurrentPrice",
					"price":  price,
				}).Info("Service")

				writeLatestPrice(price)
			}

			// Sleep an hour before retrying
			time.Sleep(1 * time.Hour)
		}
	}()
}
