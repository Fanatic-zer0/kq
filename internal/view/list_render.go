package view

import (
	"encoding/json"
	"fmt"
	"io"

	"sigs.k8s.io/yaml"
)

func RenderList(w io.Writer, result *ListResult, format string, wide bool) error {
	switch format {
	case "json":
		return encodeJSON(w, listRowsAsMaps(result, wide))
	case "yaml":
		return encodeYAML(w, listRowsAsMaps(result, wide))
	default:
		return renderListTable(w, result, wide)
	}
}

func renderListTable(w io.Writer, result *ListResult, wide bool) error {
	if len(result.Rows) == 0 {
		_, err := fmt.Fprintln(w, "No resources found.")
		return err
	}
	headers := result.Headers
	if wide {
		headers = append(append([]string{}, headers...), result.WideHeaders...)
	}
	table := NewTable(headers...)
	for _, row := range result.Rows {
		columns := row.Columns
		if wide {
			columns = append(append([]string{}, columns...), row.WideColumns...)
		}
		table.Row(columns...)
	}
	if err := table.Flush(w); err != nil {
		return err
	}
	return renderWarningsSection(w, result.Warnings)
}

func listRowsAsMaps(result *ListResult, wide bool) []map[string]string {
	headers := result.Headers
	if wide {
		headers = append(append([]string{}, headers...), result.WideHeaders...)
	}
	rows := make([]map[string]string, 0, len(result.Rows))
	for _, row := range result.Rows {
		columns := row.Columns
		if wide {
			columns = append(append([]string{}, columns...), row.WideColumns...)
		}
		entry := map[string]string{"kind": row.Ref.Kind, "name": row.Ref.Name, "namespace": row.Ref.Namespace}
		for i, header := range headers {
			if i < len(columns) {
				entry[header] = columns[i]
			}
		}
		rows = append(rows, entry)
	}
	return rows
}

func encodeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func encodeYAML(w io.Writer, value any) error {
	data, err := yaml.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode YAML: %w", err)
	}
	_, err = w.Write(data)
	return err
}
