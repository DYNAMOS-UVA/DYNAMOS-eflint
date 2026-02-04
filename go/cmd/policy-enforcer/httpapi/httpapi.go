package httpapi

import (
	"encoding/json"
	"net/http"
)

// WriteJSON writes the response as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// WriteError writes a JSON error response.
func WriteError(w http.ResponseWriter, status int, message string) {
	WriteJSON(w, status, map[string]string{"error": message})
}

// DecodeJSON decodes the request body into the provided value.
func DecodeJSON(r *http.Request, value interface{}) error {
	decoder := json.NewDecoder(r.Body)
	return decoder.Decode(value)
}
