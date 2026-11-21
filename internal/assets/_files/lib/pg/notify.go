package pg

// Notify sends a notification on channel with payload, like NOTIFY or
// pg_notify(): listeners get it when the transaction commits.
func Notify(channel, payload string) error {
	_, err := Exec("SELECT pg_catalog.pg_notify($1, $2)", channel, payload)
	return err
}
