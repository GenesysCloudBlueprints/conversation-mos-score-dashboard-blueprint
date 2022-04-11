package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/mypurecloud/platform-client-sdk-go/v65/platformclientv2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "genesys"

type Exporter struct {
	environment  string
	clientId     string
	clientSecret string
	analyticsApi *platformclientv2.AnalyticsApi
}

var (

	// Metrics
	mosScore = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "mos_score"),
		"MOS score for conversation",
		[]string{"conversationId"}, nil,
	)

	totalHits = prometheus.NewDesc(prometheus.BuildFQName(namespace, "", "total_hits"),
		"Total number of conversations received from api",
		nil, nil)

	averageMosScore = prometheus.NewDesc(prometheus.BuildFQName(namespace, "", "average_mos_score"),
		"Average MOS score",
		nil, nil)

	totalConversationsBelowThreshold = prometheus.NewDesc(prometheus.BuildFQName(namespace, "", "total_conversations_below_threshold"),
		"Total number of conversations below threshold",
		nil, nil)

	up = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "up"),
		"Was the last Genesys Cloud Analytics query successful.",
		nil, nil,
	)
)

func NewExporter(environment string, clientId string, clientSecret string, analyticsApi *platformclientv2.AnalyticsApi) *Exporter {
	return &Exporter{
		environment:  environment,
		clientId:     clientId,
		clientSecret: clientSecret,
		analyticsApi: analyticsApi,
	}
}

func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- mosScore
	ch <- up
	ch <- totalHits
	ch <- averageMosScore
	ch <- totalConversationsBelowThreshold
}

func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.readMetrics(ch)
}

func (e *Exporter) readMetrics(ch chan<- prometheus.Metric) {

	//Page number of returned records.
	pageNumber := 1

	//Maximum number of records per page
	pageSize := 200

	currentTime := time.Now()

	//interval for past 10 minutes
	interval := fmt.Sprintf("%v/%v", currentTime.Add(time.Minute*-11).Format(time.RFC3339), currentTime.Add(time.Minute*-1).Format(time.RFC3339))

	queryBody := platformclientv2.Conversationquery{
		Interval: &interval,
		ConversationFilters: &[]platformclientv2.Conversationdetailqueryfilter{
			{
				VarType: platformclientv2.String("and"),
				Predicates: &[]platformclientv2.Conversationdetailquerypredicate{
					{
						Dimension: platformclientv2.String("mediaStatsMinConversationMos"),
						Operator:  platformclientv2.String("exists"),
					},
					{
						Dimension: platformclientv2.String("conversationEnd"),
						Operator:  platformclientv2.String("exists"),
					},
				},
			},
		},
		Paging: &platformclientv2.Pagingspec{
			PageSize:   &pageSize,
			PageNumber: &pageNumber,
		},
	}

	//Api call to get initial totalHits
	conversations, _, err := e.analyticsApi.PostAnalyticsConversationsDetailsQuery(queryBody)

	if err != nil {
		ch <- prometheus.MustNewConstMetric(
			up, prometheus.GaugeValue, 0,
		)
		log.Println(err)
		return
	}

	//Update collector progress
	ch <- prometheus.MustNewConstMetric(
		up, prometheus.GaugeValue, 1,
	)

	initialTotalHits := *conversations.TotalHits

	//To check for duplicates
	collectedConversations := make(map[string]float64)

	conversationsBelowThreshold := make(map[string]float64)

	//To caculate average mos score
	mosScoreSum := 0.0

	/*Due to a 200 item per response limit, the analytics api is being called
	a couple of times based on the initial_totalHits above.
	*/
	for {
		api_response, _, err := e.analyticsApi.PostAnalyticsConversationsDetailsQuery(queryBody)

		if err != nil {
			ch <- prometheus.MustNewConstMetric(
				up, prometheus.GaugeValue, 0,
			)
			log.Println(err)
			return
		}

		if api_response.Conversations == nil {
			break
		}

		for _, v := range *api_response.Conversations {

			conversationMosScore := *v.MediaStatsMinConversationMos

			_, exists := collectedConversations[*v.ConversationId]

			if !exists {
				mosScoreSum += conversationMosScore
				if conversationMosScore <= 4.87 {
					ch <- prometheus.MustNewConstMetric(
						mosScore, prometheus.GaugeValue, conversationMosScore, *v.ConversationId,
					)
					conversationsBelowThreshold[*v.ConversationId] = conversationMosScore
				}
				collectedConversations[*v.ConversationId] = conversationMosScore
			}
		}

		pageNumber++
	}

	averageMos := mosScoreSum / float64(len(collectedConversations))

	if len(collectedConversations) == 0 {
		averageMos = 0
	}

	//Update average mos score
	ch <- prometheus.MustNewConstMetric(
		averageMosScore, prometheus.GaugeValue, averageMos,
	)

	//Update conversations below threshold
	ch <- prometheus.MustNewConstMetric(
		totalConversationsBelowThreshold, prometheus.GaugeValue, float64(len(conversationsBelowThreshold)),
	)

	//Update total hits
	ch <- prometheus.MustNewConstMetric(
		totalHits, prometheus.GaugeValue, float64(len(collectedConversations)),
	)

	log.Println("Initial total hits:", initialTotalHits)
	log.Println("Total hits:", len(collectedConversations))
	log.Println("Average MOS score:", averageMos)
	log.Println("Total conversations below threshold:", len(conversationsBelowThreshold))
	log.Println("............................................")
}

func main() {
	environment := os.Getenv("GENESYSCLOUD_REGION")
	clientId := os.Getenv("GENESYSCLOUD_OAUTHCLIENT_ID")
	clientSecret := os.Getenv("GENESYSCLOUD_OAUTHCLIENT_SECRET")

	if len(environment) <= 0 || len(clientId) <= 0 || len(clientSecret) <= 0 {
		log.Fatal("Required environment variable(s) not set")
	}

	config := platformclientv2.GetDefaultConfiguration()

	config.BasePath = fmt.Sprintf("https://api.%v", environment)

	// Rate limit configuration
	config.RetryConfiguration = &platformclientv2.RetryConfiguration{
		RetryWaitMin: time.Second * 1,
		RetryWaitMax: time.Second * 30,
		RetryMax:     20,
		RequestLogHook: func(request *http.Request, count int) {
			if count > 0 && request != nil {
				log.Printf("Retry #%d for %s %s%s", count, request.Method, request.Host, request.RequestURI)
			}
		},
	}

	err := config.AuthorizeClientCredentials(clientId, clientSecret)

	if err != nil {
		log.Panic(err)
	}

	analyticsApi := platformclientv2.NewAnalyticsApi()

	exporter := NewExporter(environment, clientId, clientSecret, analyticsApi)

	prometheus.MustRegister(exporter)

	fmt.Println("Exporter started")

	http.Handle("/metrics", promhttp.Handler())
	log.Fatal(http.ListenAndServe(":2113", nil))

}
