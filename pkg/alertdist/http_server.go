package alertdist

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/alertmanager/notify"
	"github.com/prometheus/alertmanager/template"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/promql"

	uuid "github.com/satori/go.uuid"
	"github.com/weaveworks/common/httpgrpc"
	"github.com/weaveworks/common/user"
	"github.com/weaveworks/cortex/pkg/util"
)

// PushHandler is a http.Handler which accepts WriteRequests.
// WebhookMessage
func (d *Distributor) PushHandler(w http.ResponseWriter, r *http.Request) {
	//
	userID, _, err := user.ExtractOrgIDFromHTTPRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	r = r.WithContext(user.InjectOrgID(r.Context(), userID))
	logger := util.WithContext(r.Context(), util.Logger)

	var msg notify.WebhookMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		level.Error(logger).Log("msg", "error decoding json body", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	b, err := json.Marshal(msg)
	if err != nil {
		log.Print(err)
		// return
	} else {
		log.Print(string(b))

	}

	level.Info(logger).Log("msg", "json received to alertdistnew", "json", msg.Version)

	//TODO: MMR - split the alerts, add fields and push
	// var alerts []template.Alert

	for _, alert := range msg.Alerts {

		log.Printf("alert = %v", alert)
		// alerts = append(alerts, alert)

		//Add custom kv fields for the alert
		var numCustomFields = 4
		tmpannotation := make(template.KV, len(alert.Annotations)+numCustomFields)
		//1
		tmpannotation["cortexalert_id"] = uuid.NewV4().String()
		//2
		tmpannotation["cortexalert_resolved"] = "false"
		//3
		tmpannotation["cortexalert_timestamp_milli"] = strconv.FormatInt(int64(model.Now()), 10)
		//4
		tmpannotation["cortexalert_removed"] = "false"

		for k, v := range alert.Annotations {
			tmpannotation[k] = v
		}
		alert.Annotations = tmpannotation

		//Push each alert
		if _, err := d.Push(r.Context(), &alert); err != nil {
			if httpResp, ok := httpgrpc.HTTPResponseFromError(err); ok {
				level.Error(logger).Log("msg", "push error", "err", err)
				http.Error(w, string(httpResp.Body), int(httpResp.Code))
			} else {
				level.Error(logger).Log("msg", "push error", "err", err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}

	}

	// if _, err := d.Push(r.Context(), &msg); err != nil {
	// 	if httpResp, ok := httpgrpc.HTTPResponseFromError(err); ok {
	// 		level.Error(logger).Log("msg", "push error", "err", err)
	// 		http.Error(w, string(httpResp.Body), int(httpResp.Code))
	// 	} else {
	// 		level.Error(logger).Log("msg", "push error", "err", err)
	// 		http.Error(w, err.Error(), http.StatusInternalServerError)
	// 	}
	// 	return
	// }

}

// UserStats models ingestion statistics for one user.
type UserStats struct {
	IngestionRate float64 `json:"ingestionRate"`
	NumSeries     uint64  `json:"numSeries"`
}

// UserStatsHandler handles user stats to the Distributor.
func (d *Distributor) UserStatsHandler(w http.ResponseWriter, r *http.Request) {
	stats, err := d.UserStats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	util.WriteJSONResponse(w, stats)
}

// ValidateExprHandler validates a PromQL expression.
func (d *Distributor) ValidateExprHandler(w http.ResponseWriter, r *http.Request) {
	_, err := promql.ParseExpr(r.FormValue("expr"))

	// We mimick the response format of Prometheus's official API here for
	// consistency, but unfortunately its private types (string consts etc.)
	// aren't reusable.
	if err == nil {
		util.WriteJSONResponse(w, map[string]string{
			"status": "success",
		})
		return
	}

	parseErr, ok := err.(*promql.ParseErr)
	if !ok {
		// This should always be a promql.ParseErr.
		http.Error(w, fmt.Sprintf("unexpected error returned from PromQL parser: %v", err), http.StatusInternalServerError)
		return
	}

	// If the parsing input was a single line, parseErr.Line is 0
	// and the generated error string omits the line entirely. But we
	// want to report line numbers consistently, no matter how many
	// lines there are (starting at 1).
	if parseErr.Line == 0 {
		parseErr.Line = 1
	}
	w.WriteHeader(http.StatusBadRequest)
	util.WriteJSONResponse(w, map[string]interface{}{
		"status":    "error",
		"errorType": "bad_data",
		"error":     err.Error(),
		"location": map[string]int{
			"line": parseErr.Line,
			"pos":  parseErr.Pos,
		},
	})
}
