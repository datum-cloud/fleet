package main

import (
	"errors"
	"net/http"
	"os"
	"time"
)

// labWithholdEnv names the lab-only fault hook's arm file. Only the local
// multi-plane site's Flux source (config/site) sets it, on the boot service;
// no production root may reference that source. The hook lives in this
// acceptance binary, so no production binary contains it.
const labWithholdEnv = "FLEET_LAB_BOOT_WITHHOLD"

// withhold denies every boot request while the arm file exists, so an
// admitted installation's firmware never receives its installer and the
// worker waits for a receipt until its deadline. The listener stays up, so
// the boot service stays Ready and kro still creates workers. It records no
// delivery evidence for a withheld request. Without the variable it returns
// the handler unchanged.
func withhold(next http.Handler) http.Handler {
	path := os.Getenv(labWithholdEnv)
	if path == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if _, err := os.Stat(path); err == nil {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "boot denied", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, request)
	})
}

// armWithhold creates or removes the arm file. It runs in the boot
// service's container, through the cell API.
func armWithhold(action string) error {
	path := os.Getenv(labWithholdEnv)
	if path == "" {
		return errors.New("the lab boot withhold hook is not configured")
	}
	switch action {
	case "arm":
		return os.WriteFile(path, nil, 0o600)
	case "clear":
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return errors.New("boot-withhold takes arm or clear")
}

// serveBoot listens on :8080 with the same bounds as the site's boot
// servers. The write bound covers emulated firmware fetching the initrd.
func serveBoot(handler http.Handler, window time.Duration) error {
	server := &http.Server{Addr: ":8080", Handler: withhold(handler), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: window, IdleTimeout: 5 * time.Second}
	return server.ListenAndServe()
}
