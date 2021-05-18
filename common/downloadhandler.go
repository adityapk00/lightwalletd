package common

import (
	"net/http"
	"strings"

	"github.com/sirupsen/logrus"
)

// Handle http(s) downloads for zcash params
func ParamsHandler(w http.ResponseWriter, req *http.Request) {
	if strings.HasSuffix(req.URL.Path, "sapling-output.params") {
		Metrics.TotalSaplingParamsCounter.Inc()
		Log.WithFields(logrus.Fields{
			"method": "params",
			"param":  "sapling-output",
		}).Info("ParamsHandler")

		http.Redirect(w, req, "https://z.cash/downloads/sapling-output.params", http.StatusMovedPermanently)
		return
	}

	if strings.HasSuffix(req.URL.Path, "sapling-spend.params") {
		Log.WithFields(logrus.Fields{
			"method": "params",
			"param":  "sapling-spend",
		}).Info("ParamsHandler")

		http.Redirect(w, req, "https://z.cash/downloads/sapling-spend.params", http.StatusMovedPermanently)
		return
	}

	if strings.HasSuffix(req.URL.Path, "sprout-groth16.params") {
		Log.WithFields(logrus.Fields{
			"method": "params",
			"param":  "sprout",
		}).Info("ParamsHandler")
		Metrics.TotalSproutParamsCounter.Inc()

		http.Redirect(w, req, "https://z.cash/downloads/sprout-groth16.params", http.StatusMovedPermanently)
		return
	}

	http.Error(w, "Not Found", 404)
}
