package evaluation

import "net/url"

func queryReadOnlySQLiteDSN(path string) string {
	value := url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	query.Set("mode", "ro")
	query.Set("_foreign_keys", "on")
	query.Set("_journal_mode", "WAL")
	query.Set("_synchronous", "NORMAL")
	query.Set("_busy_timeout", "5000")
	value.RawQuery = query.Encode()
	return value.String()
}
