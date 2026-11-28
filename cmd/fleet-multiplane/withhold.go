package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
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
// delivery evidence for a withheld request, but counts each denial in a file
// beside the arm file, so the lab can prove the hook withheld a boot.
// Without the variable it returns the handler unchanged.
func withhold(next http.Handler) http.Handler {
	path := os.Getenv(labWithholdEnv)
	if path == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if _, err := os.Stat(path); err == nil {
			// The count is evidence only; a failed write still denies.
			if f, err := os.OpenFile(path+deniedSuffix, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600); err == nil {
				_, _ = f.WriteString("denied\n")
				_ = f.Close()
			}
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "boot denied", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, request)
	})
}

// deniedSuffix names the file beside the arm file that counts denials.
const deniedSuffix = ".denied"

// armWithhold creates or removes the arm file, and reports how many boots
// the hook denied since it was last armed. It runs in the boot service's
// container, through the cell API.
func armWithhold(action string) error {
	path := os.Getenv(labWithholdEnv)
	if path == "" {
		return errors.New("the lab boot withhold hook is not configured")
	}
	switch action {
	case "arm":
		if err := os.Remove(path + deniedSuffix); err != nil && !os.IsNotExist(err) {
			return err
		}
		return os.WriteFile(path, nil, 0o600)
	case "denied":
		data, err := os.ReadFile(path + deniedSuffix)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Println(strings.Count(string(data), "\n"))
		return nil
	case "clear":
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return errors.New("boot-withhold takes arm, clear or denied")
}

// serveBoot listens on :8080 with the same bounds as the site's boot
// servers. The write bound covers emulated firmware fetching the initrd.
func serveBoot(handler http.Handler, window time.Duration) error {
	server := &http.Server{Addr: ":8080", Handler: withhold(handler), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: window, IdleTimeout: 5 * time.Second}
	return server.ListenAndServe()
}
