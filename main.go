package main

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36"

type Config struct {
	Symbols              []string
	UrlTemplate          string
	SkipCurrentMonth     bool
	AbsoluteDay          int
	FileLocation         string
	DeleteHistoricalData bool
	DelayBetweenRequests time.Duration
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

func newHTTPClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Timeout: 15 * time.Second,
		Jar:     jar,
	}
}

func downloadData(client *http.Client, url string, fileLocation string, fileName string) error {
	if err := os.MkdirAll(fileLocation, 0755); err != nil {
		return err
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	preview := string(body)
	if len(preview) > 256 {
		preview = preview[:256]
	}
	if strings.Contains(preview, "<!DOCTYPE html") || strings.Contains(preview, "<html") {
		return fmt.Errorf("stooq returned an HTML page instead of CSV (bot protection triggered) for URL: %s", url)
	}

	out, err := os.Create(fileLocation + fileName)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = out.Write(body)
	return err
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
	reader.LazyQuotes = true

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return recordSet, err
		}
		if len(record) < 6 {
			return recordSet, fmt.Errorf("invalid CSV format: expected at least 6 columns, got %d (first field: %q)", len(record), record[0])
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

func main() {
	config := Config{
		Symbols:              []string{"eimi.uk", "cndx.uk", "ief.us", "acwx.us"},
		UrlTemplate:          "https://stooq.com/q/d/l/?s=%s&f=%s&t=%s&i=d",
		SkipCurrentMonth:     true,
		AbsoluteDay:          0,
		FileLocation:         "./data/",
		DeleteHistoricalData: true,
		DelayBetweenRequests: 2 * time.Second,
	}

	dateFrom, dateTo, err := getDateRange(config.SkipCurrentMonth, config.AbsoluteDay)

	log.Printf("Calculating GEM for the following symbols: %s", strings.Join(config.Symbols, ", "))
	log.Printf("Date range: %s - %s", dateFrom.Format("02.01.2006"), dateTo.Format("02.01.2006"))

	if err != nil {
		log.Fatal(err)
	}

	client := newHTTPClient()

	for i, symbol := range config.Symbols {
		if i > 0 {
			log.Printf("Waiting %s before next request...", config.DelayBetweenRequests)
			time.Sleep(config.DelayBetweenRequests)
		}

		url := fmt.Sprintf(config.UrlTemplate, symbol, dateFrom.Format("20060102"), dateTo.Format("20060102"))
		filename := fmt.Sprintf("%s_%s-%s.csv", symbol, dateFrom.Format("20060102"), dateTo.Format("20060102"))

		err := downloadData(client, url, config.FileLocation, filename)
		if err != nil {
			log.Fatal(err)
		}

		recordSet, err := getRecordSet(symbol, config.FileLocation+filename)
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

	if config.DeleteHistoricalData {
		err = os.RemoveAll(config.FileLocation)
		if err != nil {
			log.Printf("There was an error during historical data removal: %s", err)
		} else {
			log.Printf("Directory with the historical data removed: %s", config.FileLocation)
		}
	}
}
