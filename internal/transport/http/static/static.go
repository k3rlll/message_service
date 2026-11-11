// Package static раздает файлы, встроенные в бинарник: демо-клиент,
// OpenAPI-спецификацию и Swagger UI. Отдельный веб-сервер для них не нужен.
package static

import (
	_ "embed"
	"net/http"

	"github.com/labstack/echo/v4"
)

var (
	//go:embed openapi.yaml
	openAPISpec []byte

	//go:embed docs.html
	docsPage []byte

	//go:embed index.html
	clientPage []byte
)

// Client - демо-клиент чата. GET /
func Client(c echo.Context) error {
	return c.Blob(http.StatusOK, echo.MIMETextHTMLCharsetUTF8, clientPage)
}

// Docs - Swagger UI. GET /docs
func Docs(c echo.Context) error {
	return c.Blob(http.StatusOK, echo.MIMETextHTMLCharsetUTF8, docsPage)
}

// OpenAPI - спецификация API. GET /openapi.yaml
func OpenAPI(c echo.Context) error {
	return c.Blob(http.StatusOK, "application/yaml", openAPISpec)
}
