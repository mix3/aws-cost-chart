package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/go-analyze/charts"
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	var (
		days   = flag.Int("d", 14, "number of days to fetch")
		output = flag.String("o", "cost_report.png", "output file path")
	)
	flag.Parse()

	dates, services, seriesCosts, err := fetchDailyCostsByService(ctx, *days)
	if err != nil {
		return fmt.Errorf("failed to fetch costs: %w", err)
	}

	if err := generateChart(dates, services, seriesCosts, *output); err != nil {
		return fmt.Errorf("failed to generate chart: %w", err)
	}

	fmt.Println("generated:", *output)
	return nil
}

func fetchDailyCostsByService(ctx context.Context, days int) (dates []string, services []string, seriesCosts [][]float64, err error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, nil, nil, err
	}

	ce := costexplorer.NewFromConfig(cfg)

	end := time.Now()
	start := end.AddDate(0, 0, -days)

	out, err := ce.GetCostAndUsage(ctx, &costexplorer.GetCostAndUsageInput{
		TimePeriod: &types.DateInterval{
			Start: aws.String(start.Format("2006-01-02")),
			End:   aws.String(end.Format("2006-01-02")),
		},
		Granularity: types.GranularityDaily,
		Metrics:     []string{"UnblendedCost"},
		GroupBy: []types.GroupDefinition{
			{
				Type: types.GroupDefinitionTypeDimension,
				Key:  aws.String("SERVICE"),
			},
		},
	})
	if err != nil {
		return nil, nil, nil, err
	}

	// 日付リストとサービス別の日別コストを収集
	serviceSet := map[string]struct{}{}
	for _, r := range out.ResultsByTime {
		dates = append(dates, *r.TimePeriod.Start)
		for _, g := range r.Groups {
			if len(g.Keys) > 0 {
				serviceSet[g.Keys[0]] = struct{}{}
			}
		}
	}

	allServices := make([]string, 0, len(serviceSet))
	for svc := range serviceSet {
		allServices = append(allServices, svc)
	}
	sort.Strings(allServices)

	costMap := map[string][]float64{}
	for _, svc := range allServices {
		costMap[svc] = make([]float64, len(dates))
	}
	for i, r := range out.ResultsByTime {
		for _, g := range r.Groups {
			if len(g.Keys) == 0 {
				continue
			}
			svc := g.Keys[0]
			var amount float64
			if v, ok := g.Metrics["UnblendedCost"]; ok {
				fmt.Sscanf(*v.Amount, "%f", &amount)
			}
			costMap[svc][i] = amount
		}
	}

	// サービスごとの合計コストを計算してソート
	type svcTotal struct {
		name  string
		total float64
	}
	totals := make([]svcTotal, 0, len(allServices))
	for _, svc := range allServices {
		var sum float64
		for _, v := range costMap[svc] {
			sum += v
		}
		totals = append(totals, svcTotal{svc, sum})
	}
	sort.Slice(totals, func(i, j int) bool {
		return totals[i].total > totals[j].total
	})

	// 上位 8 件 + Other に集約
	const topN = 8
	otherCosts := make([]float64, len(dates))

	for i, st := range totals {
		if i < topN {
			services = append(services, st.name)
			seriesCosts = append(seriesCosts, costMap[st.name])
		} else {
			for j, v := range costMap[st.name] {
				otherCosts[j] += v
			}
		}
	}

	// Other が 0 でなければ追加
	hasOther := false
	for _, v := range otherCosts {
		if v > 0 {
			hasOther = true
			break
		}
	}
	if hasOther {
		services = append(services, "Other")
		seriesCosts = append(seriesCosts, otherCosts)
	}

	return dates, services, seriesCosts, nil
}

func generateChart(dates []string, services []string, seriesCosts [][]float64, outputPath string) error {
	opt := charts.NewBarChartOptionWithData(seriesCosts)
	opt.StackSeries = charts.Ptr(true)
	opt.CategoryAxis.Labels = dates
	opt.Legend = charts.LegendOption{
		SeriesNames: services,
		Offset:      charts.OffsetRight,
	}
	p := charts.NewPainter(charts.PainterOptions{
		OutputFormat: charts.ChartOutputPNG,
		Width:        1920,
		Height:       1080,
	})
	if err := p.BarChart(opt); err != nil {
		return err
	}
	buf, err := p.Bytes()
	if err != nil {
		return err
	}
	if outputPath == "-" {
		_, err = os.Stdout.Write(buf)
		return err
	}
	return os.WriteFile(outputPath, buf, 0644)
}
