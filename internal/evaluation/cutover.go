package evaluation

import (
	"net/url"

	"goodkind.io/agent-gate/internal/auditstorage"
)

func guardEvaluationDatabasePath(path string) error {
	if err := auditstorage.GuardDatabasePath(path); err != nil {
		return wrapError("guard evaluation database cutover", err)
	}
	return nil
}

func queryReadOnlySQLiteDSN(path string) string {
	value := url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	query.Set("mode", "ro")
	value.RawQuery = query.Encode()
	return value.String()
}
