package renter

import (
	"path/filepath"
)

// CreateDir creates a directory for the renter
func (r *Renter) CreateDir(siaPath string) error {
	// Enforce nickname rules.
	if err := validateSiapath(siaPath); err != nil {
		return err
	}
	return r.createDir(filepath.Join(r.persistDir, siaPath))
}

// DeleteDir removes a directory from the renter and deletes all its sub
// directories and files from the hosts it is stored on.
//
// TODO: Implement
// func (r *Renter) DeleteDir(nickname string) error {
// 	return nil
// }

// DirList returns directories and files stored in the directory located at `path`
//
// TODO: Implement
// func (r *Renter) DirList(path string) {
// 	return
// }

// RenameDir takes an existing directory and changes the path. The original
// directory must exist, and there must not be any directory that already has
// the replacement path.  All sia files within directory will also be renamed
//
// TODO: implement, need to rename directory and walk through and rename all sia
// files within func (r *Renter) RenameDir(currentPath, newPath string) error {
//  return nil
// }
