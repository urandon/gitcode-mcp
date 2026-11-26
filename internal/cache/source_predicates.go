package cache

import "strings"

// sourcePredicates uses bound parameters for literal metadata, including label
// punctuation and SQL wildcard characters. It runs before LIMIT/top-k selection.
// JSON null (unknown legacy labels) cannot prove positive label membership.
func sourcePredicates(prefix, status, title string, labels []string, match string) (string, []any) {
	var sql strings.Builder
	var args []any
	if status != "" {
		sql.WriteString(" AND " + prefix + "status = ?")
		args = append(args, status)
	}
	if title != "" {
		sql.WriteString(" AND instr(" + prefix + "title, ?) > 0")
		args = append(args, title)
	}
	if len(labels) > 0 {
		join := " AND "
		if match == "any" {
			join = " OR "
		}
		sql.WriteString(" AND (")
		for i, label := range labels {
			if i > 0 {
				sql.WriteString(join)
			}
			sql.WriteString("EXISTS (SELECT 1 FROM json_each(" + prefix + "labels) l WHERE l.type = 'text' AND l.value = ?)")
			args = append(args, label)
		}
		sql.WriteString(")")
	}
	return sql.String(), args
}
