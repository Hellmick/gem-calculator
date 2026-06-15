package main

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Symbols              []string
	UrlTemplate          string
	SkipCurrentMonth     bool
	AbsoluteDay          int
	FileLocation         string
	DeleteHistoricalData bool
	ApiKey               string
	RiskFreeRate         float64 // annualized, e.g. 0.045 for 4.5%
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

type SymbolResult struct {
	Symbol     string
	RoR        float64
	Volatility float64
	Sharpe     float64
}

func downloadData(url string, fileLocation string, fileName string) error {
	if err := os.MkdirAll(fileLocation, 0755); err != nil {
		return err
	}

	out, err := os.Create(fileLocation + fileName)
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

			volFloat, err := strconv.ParseFloat(record[5], 64)
			if err != nil {
				return recordSet, err
			}
			volume := int64(volFloat)

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

// dailyReturns computes day-over-day percentage returns.
func (rs *RecordSet) dailyReturns() ([]float64, error) {
	if len(rs.Records) < 2 {
		return nil, errors.New("not enough data for daily returns")
	}
	returns := make([]float64, 0, len(rs.Records)-1)
	for i := 1; i < len(rs.Records); i++ {
		prev := rs.Records[i-1].Close
		curr := rs.Records[i].Close
		if prev == 0 {
			continue
		}
		returns = append(returns, (curr-prev)/prev)
	}
	return returns, nil
}

// AnnualizedVolatility returns annualized std deviation of daily returns (252 trading days).
func (rs *RecordSet) AnnualizedVolatility() (float64, error) {
	returns, err := rs.dailyReturns()
	if err != nil {
		return 0, err
	}
	if len(returns) < 2 {
		return 0, errors.New("not enough daily returns")
	}

	mean := 0.0
	for _, r := range returns {
		mean += r
	}
	mean /= float64(len(returns))

	variance := 0.0
	for _, r := range returns {
		diff := r - mean
		variance += diff * diff
	}
	variance /= float64(len(returns) - 1)

	return math.Sqrt(variance) * math.Sqrt(252) * 100, nil
}

// SharpeRatio returns annualized Sharpe ratio given a risk-free rate (annualized, e.g. 0.045).
func (rs *RecordSet) SharpeRatio(riskFreeRate float64) (float64, error) {
	returns, err := rs.dailyReturns()
	if err != nil {
		return 0, err
	}
	if len(returns) < 2 {
		return 0, errors.New("not enough daily returns")
	}

	dailyRF := riskFreeRate / 252

	mean := 0.0
	for _, r := range returns {
		mean += r
	}
	mean /= float64(len(returns))

	variance := 0.0
	for _, r := range returns {
		diff := r - mean
		variance += diff * diff
	}
	variance /= float64(len(returns) - 1)
	stdDev := math.Sqrt(variance)

	if stdDev == 0 {
		return 0, errors.New("zero standard deviation")
	}

	return ((mean - dailyRF) / stdDev) * math.Sqrt(252), nil
}

func (rs *RecordSet) SortByDate() {
	sort.Slice(rs.Records, func(i, j int) bool {
		return rs.Records[i].Date.Before(rs.Records[j].Date)
	})
}

// gemDecision implements the GEM signal:
// 1. Find the asset with the highest positive 12M RoR among equity symbols.
// 2. If the winner's RoR > 0, hold it; otherwise hold the bond (safe-haven) symbol.
// The last symbol in the list is treated as the safe-haven (bond) asset.
func gemDecision(results []SymbolResult) string {
	if len(results) < 2 {
		return "NOT ENOUGH DATA"
	}

	safeHaven := results[len(results)-1]
	equities := results[:len(results)-1]

	best := equities[0]
	for _, r := range equities[1:] {
		if r.RoR > best.RoR {
			best = r
		}
	}

	if best.RoR > 0 {
		return fmt.Sprintf("BUY / HOLD  ➜  %s  (RoR: +%.2f%%)", strings.ToUpper(best.Symbol), best.RoR)
	}
	return fmt.Sprintf("DEFENSIVE   ➜  %s  (RoR: %.2f%%)", strings.ToUpper(safeHaven.Symbol), safeHaven.RoR)
}

func printTable(results []SymbolResult) {
	fmt.Println()
	fmt.Println("┌─────────────────┬──────────────┬──────────────┬──────────────┐")
	fmt.Println("│ Symbol          │   RoR (1Y)   │  Volatility  │    Sharpe    │")
	fmt.Println("├─────────────────┼──────────────┼──────────────┼──────────────┤")
	for _, r := range results {
		rorStr := fmt.Sprintf("%+.2f%%", r.RoR)
		volStr := fmt.Sprintf("%.2f%%", r.Volatility)
		sharpeStr := fmt.Sprintf("%.2f", r.Sharpe)
		fmt.Printf("│ %-15s │ %12s │ %12s │ %12s │\n",
			strings.ToUpper(r.Symbol), rorStr, volStr, sharpeStr)
	}
	fmt.Println("└─────────────────┴──────────────┴──────────────┴──────────────┘")
	fmt.Println()
}

func forgeContext(results []SymbolResult) {
	fmt.Println("── Forge Q1 2026 Private Market Context ──────────────────────────")
	for _, r := range results {
		sym := strings.ToUpper(r.Symbol)
		switch {
		case sym == "KRU":
			fmt.Printf("  %-8s │ Private market analog: CEE Fintech/Debt Mgmt.\n", sym)
			fmt.Printf("           │ Forge Fintech basket YTD: +104.6%% (public proxy: %+.2f%%)\n", r.RoR)
			fmt.Printf("           │ KRUK intrinsic value ~PLN 545 vs market ~PLN 479 → ~14%% upside\n")
		case sym == "XTB":
			fmt.Printf("  %-8s │ Private market analog: Retail FinTech / Digital Brokerage\n", sym)
			fmt.Printf("           │ Forge Fintech basket YTD: +104.6%% (public proxy: %+.2f%%)\n", r.RoR)
			fmt.Printf("           │ Q1 2026 revenue +79%% YoY; 2.51M clients; fair value ~PLN 112\n")
		case strings.Contains(sym, "EIMI") || strings.Contains(sym, "ACWX"):
			fmt.Printf("  %-8s │ Emerging markets exposure.\n", sym)
			fmt.Printf("           │ Forge FPMI (private EM proxy) YTD: +94.7%% — private mkts outperform\n")
		case strings.Contains(sym, "CNDX") || strings.Contains(sym, "QQQ"):
			fmt.Printf("  %-8s │ Nasdaq/Tech exposure.\n", sym)
			fmt.Printf("           │ Forge AI basket YTD: +191.3%% vs public Nasdaq: %+.2f%%\n", r.RoR)
		case strings.Contains(sym, "IEF") || strings.Contains(sym, "AGG"):
			fmt.Printf("  %-8s │ Safe-haven bonds (GEM defensive asset).\n", sym)
			fmt.Printf("           │ Private markets outperformed bonds by ~89pp in 2025\n")
		default:
			fmt.Printf("  %-8s │ RoR: %+.2f%% | Sharpe: %.2f\n", sym, r.RoR, r.Sharpe)
		}
	}
	fmt.Println("──────────────────────────────────────────────────────────────────")
	fmt.Println()
}

func main() {
	config := Config{
		Symbols: []string{
			"eimi.uk",  // iShares MSCI EM IMI (Emerging Markets)
			"cndx.uk",  // iShares Nasdaq 100 UCITS
			"acwx.us",  // iShares MSCI ACWI ex US
			"kru",      // KRUK S.A. (WSE)
			"xtb",      // XTB S.A. (WSE)
			"ief.us",   // iShares 7-10Y Treasury (safe-haven / GEM bond)
		},
		UrlTemplate:          "https://stooq.com/q/d/l/?s=%s&f=%s&t=%s&i=d&apikey=%s",
		SkipCurrentMonth:     true,
		AbsoluteDay:          0,
		FileLocation:         "./data/",
		DeleteHistoricalData: true,
		ApiKey:               "d6fi8gsun1xd7MoRAzrTJCkB5SeajYyv",
		RiskFreeRate:         0.045, // ~4.5% annualized (approx. US 3M T-bill)
	}

	dateFrom, dateTo, err := getDateRange(config.SkipCurrentMonth, config.AbsoluteDay)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println()
	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Println("  GEM Calculator — Global Equity Momentum")
	fmt.Printf("  Date range : %s → %s\n", dateFrom.Format("02 Jan 2006"), dateTo.Format("02 Jan 2006"))
	fmt.Printf("  Symbols    : %s\n", strings.Join(config.Symbols, ", "))
	fmt.Printf("  Risk-free  : %.1f%% (annualized)\n", config.RiskFreeRate*100)
	fmt.Println("══════════════════════════════════════════════════════════════════")

	var results []SymbolResult

	for _, symbol := range config.Symbols {
		url := fmt.Sprintf(config.UrlTemplate, symbol, dateFrom.Format("20060102"), dateTo.Format("20060102"), config.ApiKey)
		filename := fmt.Sprintf("%s_%s-%s.csv", symbol, dateFrom.Format("20060102"), dateTo.Format("20060102"))

		err := downloadData(url, config.FileLocation, filename)
		if err != nil {
			log.Printf("ERROR downloading %s: %v", symbol, err)
			continue
		}

		recordSet, err := getRecordSet(symbol, config.FileLocation+filename)
		if err != nil {
			log.Printf("ERROR parsing %s: %v", symbol, err)
			continue
		}

		recordSet.SortByDate()

		ror, err := recordSet.RateOfReturn()
		if err != nil {
			log.Printf("ERROR RoR %s: %v", symbol, err)
			continue
		}

		vol, err := recordSet.AnnualizedVolatility()
		if err != nil {
			log.Printf("ERROR Volatility %s: %v", symbol, err)
			vol = 0
		}

		sharpe, err := recordSet.SharpeRatio(config.RiskFreeRate)
		if err != nil {
			log.Printf("ERROR Sharpe %s: %v", symbol, err)
			sharpe = 0
		}

		results = append(results, SymbolResult{
			Symbol:     symbol,
			RoR:        ror,
			Volatility: vol,
			Sharpe:     sharpe,
		})
	}

	printTable(results)

	decision := gemDecision(results)
	fmt.Println("── GEM Signal ────────────────────────────────────────────────────")
	fmt.Printf("  %s\n", decision)
	fmt.Println("──────────────────────────────────────────────────────────────────")
	fmt.Println()

	forgeContext(results)

	if config.DeleteHistoricalData {
		err = os.RemoveAll(config.FileLocation)
		if err != nil {
			log.Printf("Error removing historical data: %s", err)
		} else {
			log.Printf("Historical data cleaned up: %s", config.FileLocation)
		}
	}
}
