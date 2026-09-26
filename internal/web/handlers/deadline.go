package handlers

import (
	"log"
	"net/http"
	"time"
)

// allowLongWrite replaces the server-wide 30 s write deadline for one
// handler whose work runs longer. On HTTP/2 that deadline also cancels
// the request context, which these handlers pass to the operation, so
// without this the budget written in the service never applied.
func allowLongWrite(w http.ResponseWriter, d time.Duration) {
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d)); err != nil {
		log.Printf("extend write deadline: %v", err)
	}
}
