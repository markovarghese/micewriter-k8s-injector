package webhook

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
)

// NewHandler returns an http.Handler that decodes an AdmissionReview, calls the
// injector, and writes the mutated AdmissionReview back to the API server.
func NewHandler(injector *Injector, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			log.Error("failed to read request body", "err", err)
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		var review admissionv1.AdmissionReview
		if err := json.Unmarshal(body, &review); err != nil {
			log.Error("failed to decode AdmissionReview", "err", err)
			http.Error(w, "failed to decode AdmissionReview: "+err.Error(), http.StatusBadRequest)
			return
		}
		if review.Request == nil {
			http.Error(w, "empty AdmissionReview request", http.StatusBadRequest)
			return
		}

		review.Response = injector.Mutate(review.Request)
		review.Response.UID = review.Request.UID

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(review); err != nil {
			log.Error("failed to encode AdmissionReview response", "err", err)
		}
	})
}
