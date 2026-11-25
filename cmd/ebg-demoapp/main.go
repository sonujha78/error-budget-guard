// Command ebg-demoapp is the demo web service. See internal/demo/app.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/demo/app"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()
	srv := &http.Server{Addr: *addr, Handler: app.New().Handler(), ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "ebg-demoapp:", err)
		os.Exit(1)
	}
}
