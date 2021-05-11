package common

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
)

// Map of all historical prices. Date as "yyyy-mm-dd" to price in cents
var historicalPrices map[string]float64 = make(map[string]float64)

// The latest price
var latestPrice float64 = -1

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

	price, err := strconv.ParseFloat(priceJSON["data"].(map[string]interface{})["rateUsd"].(string), 64)
	return price, err
}

// fetchPriceFromWebAPI will fetch prices from multiple places, discard outliers and return the
// concensus price
func fetchPriceFromWebAPI() (float64, error) {
	price, err := fetchCoinCapPrice()

	// Update the historical prices if needed
	go func() {
		dt := time.Now().Format("2006-01-02")
		if _, ok := historicalPrices[dt]; !ok {
			Log.WithFields(logrus.Fields{
				"method": "HistoricalPrice",
				"action": "Add",
				"date":   dt,
				"price":  price,
			}).Info("Service")
			historicalPrices[dt] = price
		}
	}()

	return price, err
}

func GetCurrentPrice() float64 {
	return latestPrice
}

// StartPriceFetcher starts a new thread that will fetch historical and current prices
func StartPriceFetcher() {

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
				latestPrice = price
			}

			// Sleep an hour before retrying
			time.Sleep(1 * time.Hour)
		}
	}()
}
