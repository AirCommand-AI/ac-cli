//go:build !linux && !darwin

package peercred

func LockHolder(string) int { return 0 }
