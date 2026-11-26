//go:build !linux && !windows && !darwin

package artifactstore

import "os"

func openArtifactDirectory(string) (*os.File, error) { return nil, errUnsafeEntry }
func createEntry(*os.File, string) (*os.File, error) { return nil, errUnsafeEntry }
func openEntry(*os.File, string) (*os.File, error)   { return nil, errUnsafeEntry }
func publishEntry(*os.File, string, string) error    { return errUnsafeEntry }
func syncDirectory(*os.File) error                   { return errUnsafeEntry }
func removeEntry(*os.File, string) error             { return errUnsafeEntry }
