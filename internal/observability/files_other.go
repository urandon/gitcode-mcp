//go:build !darwin && !linux

package observability

import (
	"errors"
	"os"
)

var errPlatform = errors.New("observation filesystem unsupported on this platform")

func openForSync(*os.File, string) (*os.File, error) { return nil, errPlatform }

func ReadDefinition(string) ([]byte, error) { return nil, errPlatform }
func WriteDefinition(string, []byte) error  { return errPlatform }

func openDirectory(string, bool) (*os.File, error)        { return nil, errPlatform }
func lockDirectory(*os.File) (*os.File, error)            { return nil, errPlatform }
func readPrivate(*os.File, string, int64) ([]byte, error) { return nil, errPlatform }
func replacePrivate(*os.File, string, []byte) error       { return errPlatform }
func appendPrivate(*os.File, string, []byte) error        { return errPlatform }
func missing(error) bool                                  { return false }
