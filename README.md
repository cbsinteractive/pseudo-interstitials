Demo files for Demuxed 2024 Pseudo-Interstitials

# Overview 
This is the code used to demonstrate the concept of pseudo-interstitials at Demuxed 2024.
It consists of:
1. redir.go - the server for the REST commands and the path resolution.
2. beacon-client/beacon_client.go - the beacon listener/forwarder, needed for the dashboard.
3. dashboard.html - the code for the HTML dashboard allowing easy choosing of ads.
4. Misc files for help and demo defaults.  (e.g. default_ad_maps.json holds the selections for the demo, but 
since your media files will differ, it's not relevant to you.)

While the demo doesn't show this (20 minutes is pretty short), in production it would likely set 
*all* ad breaks to the same set of ads, allowing the user to jump around while gating content on the 
placement opportunity immediately preceding that content. When a user watches an ad break, that break's 
ads are frozen, new ads are bidded and set, but only plugged into the remaining unlocked breaks.

## redir server
The purpose of the redir server is to accept key-value mappings and to provide them back out, when appropriate.
Otherwise it can 302 (redirect) to the path less hostname, or can proxy to it. Keep in mind proxying this way 
is not generally going to work with SSL - it is shown for CDN equivalence mostly.

When a valid mapping is found, redir fires a beacon to the registered UDP Listener. That is currently hardcoded
(it's just a demo, remember?) to "localhost:1234". The point of this is that, at least for ads, you'll want to 
*lock* the ad block (placement opportunity or discontinuity/period) that beaconed, bid out new ads, and set all
*non-locked* ad blocks to the new ads.

The demo code isn't doing all of this, but it is firing the beacon, and then the beacon-client takes it from there.

## beacon-client
The purpose of the beacon-client is, as mentioned above, to notify when an ad segment has been requested-and-loaded.
These are, at least in our demo, distinguished by ad break; watch the Demuxed presentation for more on that.

UDP is used by the server because it is fire-and-forget, fast, lightweight, etc. If running at a CDN edge or an 
Origin Shield, UDP may not be available - i.e. most of these systems only speak HTTP. In such cases, simply change those
lines of the redir server's sendUdpBeacon( *beacon* ) method.

For this demo, we wanted a simple HTML dashboard - which also could not handle UDP. Hence the beacon client. Literally
all it does is receive from UDP, store and forward on HTTP beacons.

## dashboard
Once your redir service is running, the dashboard can be displayed at the /dashboard endpoint.
It shows the available configured ad breaks, received beacons and current mappings.
You wouldn't use this as-is in production, but it does make demoes more exciting.

# Parameters
redir.go is the server. 
    -p: Port, default is 8082
    -proxy: Enter proxy rather than redirect mode.

# Paths
It handles:
 Behavior triggers off of the request path:
     /dashboard: Shows the demo dashboard.
    */print: Print all current key-value, both to console and to socket.
    */segment: With DELETE method, removes a key-value pair
    */add: WITH PUT or POST method, adds a key-value. POST does NOT enforce idempotency. JSON body of id and dest, both strings.
    */pireplval/<id>: Redirect to the value inserted matching <id> previously.

Else: Strip host and reformat to redirect or proxy. Expects the host to end at the next slash.`

## Testing

After running, the add_segment.sh script can add a default value of:
    id="segment1"
    dest="https://2024.demuxed.com/#speakers"

These can be verify with 
    `curl http://localhost:8082/print`
 
