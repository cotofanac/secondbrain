package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"time"
)

var (
	errMissing  = errors.New("Item unavailable")
	errConflict = errors.New("Changed since this action. Refresh and try again.")
)

// Domain errors describe what the caller can do. Operational errors are logged
// here, once, rather than leaking SQL or treating an unavailable read as empty.
func writeDomainError(w http.ResponseWriter, err error, message string) {
	switch {
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, errMissing):
		http.Error(w, "Item unavailable", http.StatusNotFound)
	case errors.Is(err, errConflict):
		http.Error(w, errConflict.Error(), http.StatusConflict)
	case errors.Is(err, errBadList):
		http.Error(w, errBadList.Error(), http.StatusBadRequest)
	default:
		log.Printf("%s: %v", message, err)
		http.Error(w, message, http.StatusInternalServerError)
	}
}

func readContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}
