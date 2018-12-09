// just util which is used in auth package
package util

import (
	"encoding/json"
	"net/http"
	"strconv"
)

//ReturnHTTPError handles sending out Error response
// TODO Use the Norman API error framework instead
func ReturnHTTPError(w http.ResponseWriter, r *http.Request, httpStatus int, errorMessage string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)

	err := AuthError{
		Status:  strconv.Itoa(httpStatus),
		Message: errorMessage,
		Type:    "error",
	}

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(err)
}

func GetHTTPErrorCode(httpStatus int) string {
	switch httpStatus {
	case 401:
		return "Unauthorized"
	case 404:
		return "NotFound"
	case 403:
		return "PermissionDenied"
	case 500:
		return "ServerError"
	}

	return "ServerError"
}

//AuthError structure contains the error resource definition
type AuthError struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Message string `json:"message"`
}
