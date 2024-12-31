package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// Fetches and returns the HTML content of a URL as a Goquery document
func getContent(url string) *goquery.Document {
	res, err := http.Get(url)
	if err != nil {
		panic(err)
	}
	defer res.Body.Close()

	doc, err := goquery.NewDocumentFromReader(res.Body)
	if err != nil {
		panic(err)
	}

	return doc
}

// Validates a proxy by attempting to make a request
func validateProxy(proxy string, validProxies chan<- string, wg *sync.WaitGroup) {
	defer wg.Done()

	fmt.Printf("Validating proxy: %s\n", proxy)

	proxyPattern := `^((\d{1,3}\.){3}\d{1,3}:\d{1,5})|(\[[a-fA-F0-9:]+\]:\d{1,5})$`
	// Compile the regex
	re := regexp.MustCompile(proxyPattern)

	if re.MatchString(proxy) {
		_, err := http.Get("http://" + proxy)
		if err == nil {
			// If the proxy is valid, send it to the channel
			fmt.Printf("Proxy %s is valid\n", proxy)
			validProxies <- proxy
		} else {
			fmt.Printf("Proxy %s is not live\n", proxy)
		}
	} else {
		fmt.Printf("Proxy %s is invalid\n", proxy)
	}
}

const proxy_list_file = "proxies.txt"

func readFromFile() *os.File {

	file, err := os.OpenFile(proxy_list_file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Println("Error opening or creating file:", err)
		return nil
	}
	defer file.Close()

	return file
}

// Writes proxy to a file concurrently
func writeToFile(validProxies <-chan string, done chan<- bool) {
	file := readFromFile()

	writer := bufio.NewWriter(file)

	// Write timestamp as first line
	timestamp := fmt.Sprintf("lastUpdated: %s\n", time.Now().Format(time.RFC3339))
	_, err := writer.WriteString(timestamp)
	if err != nil {
		fmt.Println("Error writing timestamp:", err)
	}

	for proxy := range validProxies {
		_, err := writer.WriteString(proxy + "\n")
		if err != nil {
			fmt.Println("Error writing to file:", err)
		}
	}
	writer.Flush()
	done <- true
}

// Extracts proxies from the HTML document and validates them
func getProxies(doc *goquery.Document) {
	var wg sync.WaitGroup
	validProxies := make(chan string, 100) // Buffered channel to hold valid proxies
	done := make(chan bool)

	// Start a goroutine to write valid proxies to the file
	go writeToFile(validProxies, done)

	// Parse proxies from the document
	doc.Find("table tr").Each(func(i int, s *goquery.Selection) {
		if i > 0 {
			// Extract proxy details
			proxy := s.Find("td").First().Text() + ":" + s.Find("td").Eq(1).Text()

			// Increment WaitGroup counter and validate proxy in a goroutine
			wg.Add(1)
			go validateProxy(proxy, validProxies, &wg)
		}
	})

	// Wait for all validation goroutines to finish
	wg.Wait()

	// Close the valid proxies channel to signal the writer
	close(validProxies)

	// Wait for the writer to finish
	<-done
}

// Fetches and updates proxies from the URL
func updateProxies() {
	url := "https://www.sslproxies.org/"
	doc := getContent(url)
	getProxies(doc)
}

func getProxiesFromFile() []string {
	file, err := os.Open(proxy_list_file)
	if err != nil {
		fmt.Println("Error opening file:", err)
		return nil
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	proxies := make([]string, 0)

	// Skip first line (timestamp)
	if scanner.Scan() {
		fmt.Printf("Proxy list %s\n", scanner.Text())
	}

	// Read remaining lines as proxies
	for scanner.Scan() {
		proxies = append(proxies, scanner.Text())
	}

	return proxies
}

func fetchUsingProxy(proxy string, uri string) *http.Response {
	// Create a new HTTP client
	fmt.Printf("Using proxy: %s\n Fetching %s\n", proxy, uri)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(&url.URL{
				Scheme: "http",
				Host:   proxy,
			}),
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // Skip certificate verification
			},
		},
	}

	// Create a new HTTP request
	req, err := http.NewRequest("GET", uri, nil)
	if err != nil {
		fmt.Println("Error creating request:", err)
		return nil
	}

	// Set the request to use the proxy
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/114.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	req.Header.Set("Connection", "keep-alive")

	// Make the request
	res, err := client.Do(req)
	if err != nil {
		fmt.Println("Error making request:", err)
		return nil
	}

	return res
}

func main() {
	// updateProxies()
	proxies := getProxiesFromFile()

	// accept uri as parameter on /?uri=example.com
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Get URI from query parameters and decode it
		encodedUri := r.URL.Query().Get("uri")
		if encodedUri == "" {
			http.Error(w, "Missing uri parameter", http.StatusBadRequest)
			return
		}

		uri, err := url.QueryUnescape(encodedUri)
		if err != nil {
			http.Error(w, "Invalid uri parameter", http.StatusBadRequest)
			return
		}

		// Ensure we have proxies available
		if len(proxies) == 0 {
			http.Error(w, "No proxies available", http.StatusServiceUnavailable)
			return
		}

		// Add http:// prefix if not present
		if !strings.HasPrefix(uri, "http://") && !strings.HasPrefix(uri, "https://") {
			uri = "http://" + uri
		}

		// Create a copy of proxies to try
		availableProxies := make([]string, len(proxies))
		copy(availableProxies, proxies)

		// Keep trying until we get a response or run out of proxies
		for len(availableProxies) > 0 {
			// Get random index
			idx := rand.Intn(len(availableProxies))
			proxy := availableProxies[idx]

			// Remove the proxy from available list (swap with last element and slice)
			availableProxies[idx] = availableProxies[len(availableProxies)-1]
			availableProxies = availableProxies[:len(availableProxies)-1]

			res := fetchUsingProxy(proxy, uri)
			if res != nil {
				defer res.Body.Close()
				doc, err := goquery.NewDocumentFromReader(res.Body)
				if err == nil {
					fmt.Printf("Fetched content from %s using proxy %s\n", uri, proxy)
					// Instead of doc.Text(), let's get the HTML content
					html, err := doc.Html()
					if err == nil {
						w.Header().Set("Content-Type", "text/html; charset=utf-8")
						fmt.Fprint(w, html)
						return
					}
				}
			}
			// If we get here, try next proxy
		}

		// If we've tried all proxies and none worked
		http.Error(w, "Failed to fetch content with any proxy", http.StatusServiceUnavailable)
	})

	// Add route for updating proxies
	http.HandleFunc("/update-proxies", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Try to update proxies
		func() {
			defer func() {
				if r := recover(); r != nil {
					http.Error(w, fmt.Sprintf("Failed to update proxies: %v", r), http.StatusInternalServerError)
				}
			}()

			updateProxies()

			// Refresh the global proxies slice
			proxies = getProxiesFromFile()

			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "Successfully updated proxies. Total proxies: %d", len(proxies))
		}()
	})

	// Add route for checking proxy status
	http.HandleFunc("/proxy-status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		file, err := os.Open(proxy_list_file)
		if err != nil {
			http.Error(w, "Failed to open proxy file", http.StatusInternalServerError)
			return
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)

		// Get timestamp from first line
		var lastUpdated string
		if scanner.Scan() {
			lastUpdated = scanner.Text()
		}

		// Count remaining lines (proxies)
		proxyCount := 0
		for scanner.Scan() {
			proxyCount++
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"status": "ok",
			"lastUpdated": "%s",
			"proxyCount": %d,
			"proxies": %v
		}`, lastUpdated, proxyCount, proxies)
	})

	fmt.Println("Server is ready to listen on :8080")
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		fmt.Printf("Server failed to start: %v\n", err)
	}
}
