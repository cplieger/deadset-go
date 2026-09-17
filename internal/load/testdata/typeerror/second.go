package typeerror

// Undefined calls a function this package does not declare, so the load reports
// a second diagnostic in a second file.
func Undefined() { missingFunction() }
