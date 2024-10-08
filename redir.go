package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed dashboard.html
var dashboardTemplate string

//go:embed default_ads_map.json
var defaultAdsMapJson string

// Configured client for UDP Messages, including port in addr:port format
var udpListener string = "localhost:1234"

// String template for break segments, with the following interpolatables:
// %bn% - Break Number, 1-based. e.g. first ad break.
// %ut% - User Token for personalization. Keep it manifest-legal. Optional
// %sn% - Segment Number inside that Ad Break. 1-based also.
const breakBaseName string = "%bn%adbreak%ut%_%sn%"

const (
	defaultServerPort = 8082
	defaultProxyMode  = false
)

var (
	segmentMappings = map[string]string{}
	piReplValRegexp = regexp.MustCompile(`/pireplval/(?P<key_value>.+)`)
	adBreakPrefixes = []string{"1adbreak", "2adbreak", "3adbreak", "4adbreak"}
	adBreakMappings = map[string]string{}
	adsMap          = map[string][]string{}
)

const help string = `Pseudo-Interstitial Server for Demuxed demo
Behavior triggers off of the request path:
*/add: Add a key-value. Post-only, JSON body of id and dest, both strings.
*/print: Print all current key-value, both to console and to socket.
*/pireplval/<id>: Redirect to the value inserted matching <id> previously.
Else: Strip host and reformat to redirect or proxy. Expects the host to end at the next slash.`

type segmentMap struct {
	ID   string `json:"id"`
	Dest string `json:"dest"`
}

// Structure for a single ad
type ads struct {
	// What to call this entire set
	name string
	// How long to keep it, optional. Doesn't expire if omitted. Not currently used.
	expireSecs int
	// List of segments. init.mp4 first if applicable
	segments []string
	// Ad break to assign to.
	breakName string
}

func iif(cond bool, yes string, no string) string {
	if cond {
		return yes
	}
	return no
}

func getAdsMap() (returnMap map[string][]string, err error) {
	returnMap = map[string][]string{}
	err = json.Unmarshal([]byte(defaultAdsMapJson), &returnMap)
	return
}

// Interpolates name for ad segment link in manifest.
func buildAdSegmentLink(breakNum int, userToken string, segNum int) string {
	destString := strings.Replace(breakBaseName, "%bn%", iif(breakNum > 0, strconv.Itoa(breakNum), ""), 1)
	destString = strings.Replace(destString, "%ut%", userToken, 1)
	return strings.Replace(destString, "%sn%", strconv.Itoa(segNum), 1)
}

type TemplateData struct {
	SegmentNames    []string
	SegmentMappings map[string]string
	AdBreaks        []string // New field for available ad-breaks
	AdBreakMappings map[string]string
}

// Return the current status of the segment + adbreak mappings.
// This must be returned by the all REST endpoints called within
// the dashboard page, in order to properly refresh the DOM elements.
func gatherTemplateData() TemplateData {
	// get the sorted list of segment-names
	segmentNames := []string{}
	for k := range segmentMappings {
		segmentNames = append(segmentNames, k)
	}
	sort.SliceStable(segmentNames, func(i, j int) bool {
		return segmentNames[i] < segmentNames[j]
	})

	return TemplateData{SegmentNames: segmentNames, SegmentMappings: segmentMappings, AdBreaks: adBreakPrefixes, AdBreakMappings: adBreakMappings}
}

// Map actual ad segments into segment links.
// segmentTemplate - base filename, without extension. This assumes mp4 init, m4s segments.
// hasInit - true if first replacement is for {name}_init.mp4 rather than {name}{segnum}.m4s
// e.g. BBB might be packaged to BBB_init.mp4 and BBB1.m4s ... BBB140.m4s
func addAdByTemplate(name string, segmentTemplate string, segmentCount int, destinationBreak int, hasInit bool) {
	segNum := 1
	if hasInit {
		id := buildAdSegmentLink(destinationBreak, "", segNum)
		segNum++
		dest := fmt.Sprintf("%s_init.mp4", segmentTemplate)
		segmentMappings[id] = dest
	}
	for i := 0; i < segmentCount; i++ {
		id := buildAdSegmentLink(destinationBreak, "", segNum)
		segNum++
		dest := fmt.Sprintf("%s%d.m4s", segmentTemplate, i)
		segmentMappings[id] = dest
	}
}

// If PIToken is embedded in the path, looks it up and returns target, key and nil.
// If PIToken embedded but key not found, returns original path, key and error.
// If PIToken not found, returns path, empty and nil.
func redirectLookup(path string) (targetURL string, token string, err error) {
	matches := piReplValRegexp.FindStringSubmatch(path)
	if len(matches) == 0 {
		return "http:/" + path, "", nil
	}

	hashMap := map[string]string{}
	for idx, sub := range piReplValRegexp.SubexpNames() {
		if idx > 0 && idx < len(matches) {
			hashMap[sub] = matches[idx]
		}
	}
	key := hashMap["key_value"]
	targetURL, ok := segmentMappings[key]
	if !ok {
		return path, key, errors.New("segment-key not in map: %s" + key)
	}
	return targetURL, key, nil
}

// Checks the incoming request for the replacement token. If found, redirects to that. Otherwise directs to specified path.
func redirectHandler(proxyMode bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, fmt.Sprintf("Bad request method for pireplval: %s", r.Method), http.StatusBadRequest)
			return
		}
		targetURL, key, err := redirectLookup(r.URL.Path)
		if err != nil {
			errString := fmt.Sprintf("error looking up redirect for %s on key %s: %s", r.URL.Path, key, err.Error())
			http.Error(w, errString, http.StatusInternalServerError)
			log.Print(errString)
			return
		}
		if len(key) > 0 {
			sendUdpBeacon(key)
		}
		if proxyMode {
			proxyRequest(w, r, targetURL)
		} else {
			http.Redirect(w, r, targetURL, http.StatusSeeOther)
		}
	})
}

func printHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, fmt.Sprintf("Bad request method for add-value: %s", r.Method), http.StatusBadRequest)
			return
		}

		segmentMappingsJSON, err := json.Marshal(segmentMappings)
		if err != nil {
			errString := fmt.Sprintf("Could not marshall segment-mappings +%v: %s", segmentMappings, err.Error())
			http.Error(w, errString, http.StatusInternalServerError)
			log.Print(errString)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, string(segmentMappingsJSON))
	})
}

// given a break-id (i.e. 1adbreak) and an ad (i.e. "Spikes"),
// clearout the previous segments for that break-id, and
// assign new, 1-indexed segments from the ad-segments
func mapAdToAdBreakHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut && r.Method != http.MethodPost {
			http.Error(w, fmt.Sprintf("Bad request method for add-value: %s", r.Method), http.StatusBadRequest)
			return
		}
		// get body as map
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			errString := fmt.Sprintf("Could not read request body +%v: %s", r.Body, err.Error())
			http.Error(w, errString, http.StatusBadRequest)
			log.Print(errString)
			return
		}
		bodyMap := map[string]string{}
		err = json.Unmarshal(bodyBytes, &bodyMap)
		if err != nil {
			errString := fmt.Sprintf("Could not unmarshal body-bytes into map %s: %s", string(bodyBytes), err.Error())
			http.Error(w, errString, http.StatusBadRequest)
			log.Print(errString)
			return
		}

		breakID := bodyMap["break_id"]
		ad := bodyMap["ad"]

		// update ad-break mappings
		if ad == "None" {
			delete(adBreakMappings, breakID)
		} else {
			adBreakMappings[breakID] = ad
		}

		// clear out privous segmentMappings
		for k := range segmentMappings {
			if !strings.HasPrefix(k, breakID) {
				continue
			}
			// clear out old mappings
			delete(segmentMappings, k)
		}

		adSegments := adsMap[ad]
		for segmentIdx, segment := range adSegments {
			segmentMappings[fmt.Sprintf("%s_%d", breakID, segmentIdx+1)] = segment
		}

		w.Header().Set("Content-Type", "application/json")
		templateData := gatherTemplateData()
		templateDataBytes, err := json.Marshal(templateData)
		if err != nil {
			http.Error(w, "Error rendering templateData: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, string(templateDataBytes))
	})
}

func segmentMappingHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut || r.Method == http.MethodPost {
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				errString := fmt.Sprintf("Could not read request body +%v: %s", r.Body, err.Error())
				http.Error(w, errString, http.StatusBadRequest)
				log.Print(errString)
				return
			}

			_, err = addToSegmentMappings(bodyBytes)
			if err != nil {
				errMsg := fmt.Sprintf("invalid segment-mapping JSON: +%v", r.Body)
				http.Error(w, errMsg, http.StatusBadRequest)
				log.Print(errMsg)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			templateData := gatherTemplateData()
			templateDataBytes, err := json.Marshal(templateData)
			if err != nil {
				http.Error(w, "Error rendering templateData: "+err.Error(), http.StatusInternalServerError)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, string(templateDataBytes))
			return
		} else if r.Method == http.MethodDelete {
			if r.Method != http.MethodDelete {
				http.Error(w, fmt.Sprintf("Bad request method for delete-value: %s", r.Method), http.StatusBadRequest)
				return
			}
			key := strings.TrimPrefix(r.URL.Path, "/segment/")
			if key == "" {
				http.Error(w, "missing key for DELETE segment resource", http.StatusBadRequest)
				return
			}
			delete(segmentMappings, key)
			w.Header().Set("Content-Type", "application/json")
			templateData := gatherTemplateData()
			templateDataBytes, err := json.Marshal(templateData)
			if err != nil {
				http.Error(w, "Error rendering templateData: "+err.Error(), http.StatusInternalServerError)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, string(templateDataBytes))
			return
		}
		http.Error(w, fmt.Sprintf("Bad request method for segment resource: %s", r.Method), http.StatusBadRequest)
	})
}

func printUsage() {
	fmt.Println(help)
	flag.PrintDefaults()
}

// decode post-body, add to mappings
func addToSegmentMappings(bodyBytes []byte) (mapping segmentMap, err error) {
	err = json.Unmarshal(bodyBytes, &mapping)
	if err != nil {
		return
	}
	segmentMappings[mapping.ID] = mapping.Dest
	log.Println("Updated mapping: ", mapping.ID, " to ", mapping.Dest)
	return
}

/* Copies the headers and sends the request off, and then copies the results back to the original */
func proxyRequest(respwriter http.ResponseWriter, req *http.Request, targetURL string) {
	var proxyTransport = http.DefaultTransport
	proxyReq, err := http.NewRequest(req.Method, targetURL, req.Body)
	if err != nil {
		http.Error(respwriter, "Could not create proxy", http.StatusInternalServerError)
		return
	}

	// Copy the headers from the original request to the proxy request
	for name, values := range req.Header {
		for _, value := range values {
			proxyReq.Header.Add(name, value)
		}
	}

	resp, err := proxyTransport.RoundTrip(proxyReq)
	if err != nil {
		http.Error(respwriter, "Error sending proxy request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	for name, values := range resp.Header {
		for _, value := range values {
			respwriter.Header().Add(name, value)
		}
	}
	respwriter.WriteHeader(resp.StatusCode)
	io.Copy(respwriter, resp.Body) //nolint:errcheck
}

func renderDashboardHandler() http.Handler {
	return http.HandlerFunc(renderDashboard)
}

/* Renders the current mapping state */
func renderDashboard(respwriter http.ResponseWriter, req *http.Request) {
	renderAsJson := strings.HasSuffix(req.URL.Path, ".json")

	// Parse and execute the templ
	templ := template.Must(template.New("page").Parse(dashboardTemplate))
	if renderAsJson {
		templateData := gatherTemplateData()
		templateDataBytes, err := json.Marshal(templateData)
		if err != nil {
			http.Error(respwriter, "Error rendering templateData: "+err.Error(), http.StatusInternalServerError)
			return
		}
		respwriter.Header().Set("Content-Type", "application/json")
		fmt.Fprint(respwriter, string(templateDataBytes))
	} else {
		respwriter.Header().Set("Content-Type", "text/html")
		templ.Execute(respwriter, gatherTemplateData()) //nolint:errcheck
	}
}

func loadSegDefs() {
	addAdByTemplate("Arrow", "http://localhost:8080/ads/BigBuck_Arrow", 3, 1, true)
	addAdByTemplate("Spikes", "http://localhost:8080/ads/BigBuck_Spikes", 3, 2, true)
}

// If we have the client listener configured, this sends a beacon on replacment segments of the segment id being replaced.
func sendUdpBeacon(message string) {
	if len(udpListener) < 8 {
		return
	}
	addr, err := net.ResolveUDPAddr("udp", udpListener)
	if err != nil {
		panic(err)
	}

	// Create a UDP connection
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		fmt.Printf("UDP Beacon Error: Could not resolve %s\n", udpListener)
		return
	}
	defer conn.Close()

	_, err = conn.Write([]byte(message))
	if err != nil {
		fmt.Printf("UDP Beacon Error: Could not send to %s\n", udpListener)
	}
}

func main() {
	flag.Usage = printUsage
	// Define the flag with default value "single" and a description
	portArg := flag.Int("p", defaultServerPort, "server port")

	proxyMode := flag.Bool("proxy", defaultProxyMode, "Proxy rather than redirect (default false)")
	flag.Parse()

	serverPort := defaultServerPort
	if portArg != nil {
		serverPort = *portArg
	}

	http.Handle("/dashboard", renderDashboardHandler())
	http.Handle("/dashboard.json", renderDashboardHandler())
	http.Handle("/segment/", segmentMappingHandler())
	http.Handle("/segment", segmentMappingHandler())

	http.Handle("/map", mapAdToAdBreakHandler())
	http.Handle("/print", printHandler())
	http.Handle("/", redirectHandler(proxyMode != nil && *proxyMode))
	// set new segment mappings
	var adsMapErr error
	adsMap, adsMapErr = getAdsMap()
	if adsMapErr != nil {
		log.Print("Warning: failed to loads default ads-map file: %s", adsMapErr.Error())
	}

	log.Printf("Starting Redir server on :%d, proxy=%v\n", serverPort, (proxyMode != nil && *proxyMode))
	err := http.ListenAndServe(fmt.Sprintf(":%d", serverPort), nil)
	if err != nil {
		log.Fatal("Error starting proxy server: ", err)
	}
}
