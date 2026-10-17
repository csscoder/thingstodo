package repository

import "encoding/json"

// logChange is a convenience wrapper used by all repositories to record a mutation.
// It is nil-safe: if cl is nil (e.g., in tests), the call is a no-op.
// Marshal failures stay best-effort; the AppendChange error is returned so
// code running inside inTx can propagate it.
func logChange(cl *ChangeLogRepository, entity, entityID, action string, fields []string, snapshot interface{}, userID, deviceID string) error {
	if cl == nil {
		return nil
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil // best-effort logging
	}
	var fieldsJSON *string
	if fields != nil {
		f, _ := json.Marshal(fields)
		s := string(f)
		fieldsJSON = &s
	}
	_, err = cl.AppendChange(entity, entityID, action, fieldsJSON, string(data), userID, deviceID)
	return err
}
