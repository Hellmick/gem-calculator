# GEM CLI - Go Equity Market Analyzer

A simple command-line tool written in Go to fetch historical stock/ETF data from [Stooq](https://stooq.com), calculate the **Rate of Return (RoR)**, and output results for multiple symbols over a configurable date range.

## Features

- Fetch daily historical data (CSV) for multiple symbols.
- Configurable date range with options:
  - Skip the current month.
  - Use an absolute day of the month as the end date.
- Automatically sorts data by date before calculations.
- Computes **Rate of Return (RoR)** for each symbol.
- Logs output with timestamps using Go’s `log` package.
- Safe HTTP requests with timeout and CSV parsing.
- Designed for CLI use with reusable configuration struct.

## Installation

1. Clone the repository:

```bash
git clone https://github.com/Hellmick/gem-calculator.git
cd gem-calculator
```
2. Build the binary:
```bash
go build -o gemc
```
3. Run:
```bash
./gemc
```
