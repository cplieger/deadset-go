package graph

import (
	"go/ast"
	"strings"
)

// swagRouteAnnotation opens the line of a doc comment through which swag documents
// the function below it as an API operation: https://github.com/swaggo/swag#api-operation
const swagRouteAnnotation = "@Router"

// swagRoute reports whether a doc comment holds a line that opens with the swag
// route annotation followed by a space or a tab. swag reads the operation from
// that comment whether or not anything calls the function.
func swagRoute(doc *ast.CommentGroup) bool {
	for line := range docLines(doc) {
		rest, ok := strings.CutPrefix(line, swagRouteAnnotation)
		if ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			return true
		}
	}
	return false
}
