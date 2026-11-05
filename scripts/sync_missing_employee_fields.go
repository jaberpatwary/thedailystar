package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var phonePattern = regexp.MustCompile(`(?:(?:\+88|0088)?0?1[3-9][0-9]{8}|1[3-9][0-9]{8})`)
var idPattern = regexp.MustCompile(`(?i)ID:\s*((?:[A-Za-z]+-)?[0-9]+)\b`)

func main() {
	csvPath, indexPath := `data/employees.csv`, `data/employees_index.json`
	if len(os.Args) > 1 {
		csvPath = os.Args[1]
	}
	if len(os.Args) > 2 {
		indexPath = os.Args[2]
	}
	if err := syncFields(csvPath, indexPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func syncFields(csvPath, indexPath string) error {
	f, err := os.Open(csvPath)
	if err != nil {
		return err
	}
	rows, err := csv.NewReader(f).ReadAll()
	f.Close()
	if err != nil {
		return err
	}
	if len(rows) < 2 {
		return fmt.Errorf("CSV has no employee data rows")
	}
	columns := make(map[string]int)
	for i, header := range rows[0] {
		columns[strings.ToLower(strings.TrimSpace(header))] = i
	}
	get := func(row []string, field string) string {
		if i, ok := columns[field]; ok && i < len(row) {
			return strings.TrimSpace(row[i])
		}
		return ""
	}
	sourceByID := make(map[string]map[string]string)
	for _, row := range rows[1:] {
		id := strings.ToLower(get(row, "hr_id"))
		if id == "" {
			if match := idPattern.FindStringSubmatch(get(row, "extension")); len(match) == 2 {
				id = strings.ToLower(match[1])
			}
		}
		if id == "" {
			continue
		}
		sourceByID[id] = map[string]string{
			"blood_group":     get(row, "blood_group"),
			"extension":       get(row, "extension"),
			"whatsapp_number": get(row, "whatsapp_number"),
			"work_station":    get(row, "work_station"),
			"section":         get(row, "section"),
		}
	}

	indexBytes, err := os.ReadFile(indexPath)
	if err != nil {
		return err
	}
	var employees []map[string]json.RawMessage
	if err := json.Unmarshal(indexBytes, &employees); err != nil {
		return err
	}
	readString := func(record map[string]json.RawMessage, field string) string {
		var value string
		_ = json.Unmarshal(record[field], &value)
		return value
	}
	writeString := func(record map[string]json.RawMessage, field, value string) {
		record[field], _ = json.Marshal(value)
	}
	groupCount, phoneCount, whatsappCount, stationCount, sectionCount := 0, 0, 0, 0, 0
	for _, employee := range employees {
		id := strings.ToLower(readString(employee, "employee_id"))
		if id == "" {
			if match := idPattern.FindStringSubmatch(readString(employee, "extension")); len(match) == 2 {
				id = strings.ToLower(match[1])
			}
		}
		source, ok := sourceByID[id]
		if !ok {
			continue
		}
		if strings.TrimSpace(readString(employee, "blood_group")) == "" && source["blood_group"] != "" {
			writeString(employee, "blood_group", source["blood_group"])
			groupCount++
		}
		oldExtension := readString(employee, "extension")
		newExtension := source["extension"]
		if extractPhone(oldExtension) == "" && extractPhone(newExtension) != "" {
			writeString(employee, "extension", newExtension)
			oldExtension = newExtension
			phoneCount++
		}
		if strings.TrimSpace(readString(employee, "phone")) == "" {
			if phone := extractPhone(oldExtension); phone != "" {
				writeString(employee, "phone", phone)
			}
		}
		if strings.TrimSpace(readString(employee, "whatsapp_number")) == "" && source["whatsapp_number"] != "" {
			writeString(employee, "whatsapp_number", source["whatsapp_number"])
			whatsappCount++
		}
		if strings.TrimSpace(readString(employee, "work_station")) == "" && source["work_station"] != "" {
			writeString(employee, "work_station", source["work_station"])
			stationCount++
		}
		if strings.TrimSpace(readString(employee, "section")) == "" && source["section"] != "" {
			writeString(employee, "section", source["section"])
			sectionCount++
		}
	}

	updated, err := json.MarshalIndent(employees, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(indexPath, updated, 0644); err != nil {
		return err
	}
	fmt.Printf("Index records: %d; missing blood groups filled: %d; phone fields filled: %d; WhatsApp fields filled: %d; work stations added: %d; sections added: %d\n", len(employees), groupCount, phoneCount, whatsappCount, stationCount, sectionCount)
	return nil
}

func extractPhone(value string) string {
	compact := strings.NewReplacer("-", "", " ", "", ".", "", "(", "", ")", "").Replace(value)
	phone := phonePattern.FindString(compact)
	if len(phone) == 10 && phone[0] == '1' {
		phone = "0" + phone
	}
	return phone
}
