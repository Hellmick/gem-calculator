package main

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Symbols          []string
	UrlTemplate      string
	SkipCurrentMonth bool
	AbsoluteDay      int
	FileLocation     string
}

type RecordSet struct {
	Symbol  string
	Records []DayRecord
}

type DayRecord struct {
	Date   time.Time
	Close  float64
	Volume int64
}

func downloadData(url string, filePath string) error {
	if err := os.MkdirAll(filePath, 0755); err != nil {
		return err
	}

	out, err := os.Create(filePath)
	if err != nil {
		return err
	}

	defer out.Close()
	client := http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Get(url)
	if err != nil {
		return err
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return err
	}

	return nil
}

func clampDay(t time.Time, day int) time.Time {
	y, m, _ := t.Date()
	lastDay := time.Date(y, m+1, 0, 0, 0, 0, 0, t.Location()).Day()
	if day > lastDay {
		day = lastDay
	}

	return time.Date(y, m, day, 0, 0, 0, 0, t.Location())
}

func getDateRange(skipCurrentMonth bool, absoluteDay int) (time.Time, time.Time, error) {
	dateTo := time.Now()

	if skipCurrentMonth {
		dateTo = dateTo.AddDate(0, -1, 0)
	}

	if absoluteDay < 0 {
		return time.Time{}, time.Time{}, errors.New("absolute day must be positive")
	}

	if absoluteDay > 0 {
		dateTo = clampDay(dateTo, absoluteDay)
	}

	dateFrom := dateTo.AddDate(-1, 0, 0)

	return dateFrom, dateTo, nil
}

func getRecordSet(symbol string, path string) (*RecordSet, error) {
	recordSet := &RecordSet{Symbol: symbol, Records: []DayRecord{}}

	f, err := os.Open(path)
	if err != nil {
		return recordSet, err
	}

	defer f.Close()
	reader := csv.NewReader(f)

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return recordSet, err
		}
		if len(record) < 6 {
			return recordSet, errors.New("invalid CSV format: expected at least 6 columns")
		}

		if record[0] != "Date" {

			date, err := time.Parse("2006-01-02", record[0])
			if err != nil {
				return recordSet, err
			}

			close, err := strconv.ParseFloat(record[4], 64)
			if err != nil {
				return recordSet, err
			}

			volume, err := strconv.ParseInt(record[5], 10, 64)
			if err != nil {
				return recordSet, err
			}

			dayRecord := DayRecord{Date: date, Close: close, Volume: volume}
			recordSet.Records = append(recordSet.Records, dayRecord)

		}
	}

	return recordSet, err
}

func (rs *RecordSet) RateOfReturn() (float64, error) {
	if len(rs.Records) > 1 {
		initialValue := rs.Records[0].Close
		if initialValue == 0 {
			return 0, errors.New("initial value is zero")
		}

		currentValue := rs.Records[len(rs.Records)-1].Close

		return ((currentValue - initialValue) / initialValue) * 100, nil
	}

	return 0, errors.New("not enough data")
}

func (rs *RecordSet) SortByDate() {
	sort.Slice(rs.Records, func(i, j int) bool {
		return rs.Records[i].Date.Before(rs.Records[j].Date)
	})
}

/*
func getValueChange(recordSet *RecordSet) {
	currentValue := recordSet.Records[len(recordSet.Records)-1].Close
	initialValue := recordSet.Records[0].Close
	recordSet.RoR = ((currentValue - initialValue) / initialValue) * 100
}*/

func main() {

	config := Config{
		Symbols:          []string{"eimi.uk", "cndx.uk", "ief.us", "acwx.us"},
		UrlTemplate:      "https://stooq.com/q/d/l/?s=%s&f=%s&t=%s&i=d",
		SkipCurrentMonth: true,
		AbsoluteDay:      0,
		FileLocation:     "./data/",
	}

	dateFrom, dateTo, err := getDateRange(config.SkipCurrentMonth, config.AbsoluteDay)
	dateFromStr := dateFrom.Format("20060102")
	dateToStr := dateTo.Format("20060102")

	log.Printf("Calculating GEM for the following symbols: %s", strings.Join(config.Symbols, ", "))
	log.Printf("Date range: %s - %s", dateFromStr, dateToStr)

	if err != nil {
		log.Fatal(err)
	}

	for _, symbol := range config.Symbols {
		url := fmt.Sprintf(config.UrlTemplate, symbol, dateFromStr, dateToStr)
		filename := fmt.Sprintf("%s_%s-%s.csv", symbol, dateFromStr, dateToStr)
		filePath := fmt.Sprintf("%s%s", config.FileLocation, filename)

		err := downloadData(url, filePath)
		if err != nil {
			log.Fatal(err)
		}

		recordSet, err := getRecordSet(symbol, filePath)
		if err != nil {
			log.Fatal(err)
		}

		recordSet.SortByDate()
		ror, err := recordSet.RateOfReturn()
		if err != nil {
			log.Fatal(err)
		}

		log.Printf("symbol=%s ror=%.2f%%", recordSet.Symbol, ror)
	}
}
