package transcode

import (
	"errors"
	"net/http"
)

var errInputExhausted = errors.New("transcode: input failed after bounded retries")

func writeStartError(w http.ResponseWriter, err error) {
	if errors.Is(err, errInputExhausted) {
		http.Error(w, "source unavailable", http.StatusBadGateway)
		return
	}
	http.Error(w, "transcoder failed to start", http.StatusInternalServerError)
}
