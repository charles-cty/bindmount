//go:build windows

package main

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
)

func TestShouldFallbackSiloLaunch(t *testing.T) {
	jobListErr := allowSiloFallback(fmt.Errorf("JOB_LIST: %w", errorInvalidParameter))
	if !shouldFallbackSiloLaunch(jobListErr) {
		t.Fatal("marked JOB_LIST failure should allow fallback")
	}
	if !shouldFallbackSiloLaunch(fmt.Errorf("wrapped: %w", jobListErr)) {
		t.Fatal("wrapped marked failure should allow fallback")
	}

	for _, err := range []error{
		nil,
		errors.New("WaitForSingleObject failed"),
		syscall.ERROR_FILE_NOT_FOUND,
		errorInvalidParameter,
	} {
		if shouldFallbackSiloLaunch(err) {
			t.Errorf("unmarked error %v should not allow fallback", err)
		}
	}
}
