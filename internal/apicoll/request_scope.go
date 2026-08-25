package apicoll

// RequestVariableRow is one request-scope row, including the folder source
// metadata that ReadRequest attaches for resolution. From is empty for a
// request-owned row and is the folder path for an inherited row.
type RequestVariableRow struct {
	Name      string
	Value     string
	Enabled   bool
	From      string
	Inherited bool
}

// RequestVariableRows returns request-owned rows followed by inherited folder
// rows in the exact order RequestLookup consumes them. It is the read-side
// projection for callers that need to explain resolution without rebuilding
// the request chain.
func RequestVariableRows(r Request) []RequestVariableRow {
	rows := make([]RequestVariableRow, 0, len(r.Variables)+len(r.folderVariables))
	for _, variable := range r.Variables {
		rows = append(rows, RequestVariableRow{
			Name: variable.Name, Value: variable.Value, Enabled: variable.Enabled,
		})
	}
	for i, variable := range r.folderVariables {
		from := ""
		if i < len(r.folderVariableSources) {
			from = r.folderVariableSources[i]
		}
		rows = append(rows, RequestVariableRow{
			Name: variable.Name, Value: variable.Value, Enabled: variable.Enabled,
			From: from, Inherited: true,
		})
	}
	return rows
}
