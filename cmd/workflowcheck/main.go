package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/bootstrap"
)

type apiResponse struct {
	Status int
	Body   map[string]any
	Raw    string
}

type harness struct {
	baseURL string
	client  *http.Client
	server  *httptest.Server
}

func main() {
	workflow := flag.String("workflow", "", "workflow id to verify")
	flag.Parse()

	checks := map[string]func() error{
		"register-device":             checkRegisterDevice,
		"open-verification-case":      checkOpenVerificationCase,
		"record-measurement":          checkRecordMeasurement,
		"reopen-verification-case":    checkReopenVerificationCase,
		"seal-verification":           checkSealVerification,
		"reviewer-qualification-seal": checkReviewerQualificationSeal,
	}
	run, ok := checks[*workflow]
	if !ok {
		fmt.Fprintf(flag.CommandLine.Output(), "unknown workflow %q\n", *workflow)
		flag.Usage()
		os.Exit(2)
	}
	if err := run(); err != nil {
		fmt.Printf("workflow %s: failed: %v\n", *workflow, err)
		os.Exit(1)
	}
	fmt.Printf("workflow %s: ok\n", *workflow)
}

func checkRegisterDevice() error {
	h := newHarness()
	defer h.close()

	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": " met-1001 ",
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return err
	}
	id, err := nestedString(response.Body, "device", "id")
	if err != nil {
		return err
	}
	tag, err := nestedString(response.Body, "device", "asset_tag")
	if err != nil {
		return err
	}
	if tag != "MET-1001" {
		return fmt.Errorf("asset tag was not normalized: %q", tag)
	}

	read, err := h.request(http.MethodGet, "/devices/"+id, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	readID, err := nestedString(read.Body, "device", "id")
	if err != nil {
		return err
	}
	if readID != id {
		return fmt.Errorf("read returned device %q, expected %q", readID, id)
	}

	duplicate, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": "MET-1001",
		"model":     "Other Meter",
		"room":      "Room B",
		"steward":   "Mira",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(duplicate, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(duplicate, "duplicate_asset_tag")
}

func checkOpenVerificationCase() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1101")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	state, err := nestedString(opened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("opened case state = %q", state)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}

	second, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(second, "active_case_exists")
}

func checkRecordMeasurement() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1201")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	accepted, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(accepted, http.StatusCreated); err != nil {
		return err
	}
	state, err := nestedString(accepted.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "review_ready" {
		return fmt.Errorf("accepted measurement state = %q", state)
	}
	acceptedValue, err := nestedBool(accepted.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if !acceptedValue {
		return errors.New("in-tolerance measurement was not accepted")
	}

	second, err := h.recordMeasurement(caseID, 25.1, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	rejectDeviceID, err := h.createDevice("MET-1202")
	if err != nil {
		return err
	}
	rejectCase, err := h.openCase(rejectDeviceID)
	if err != nil {
		return err
	}
	rejectCaseID, err := nestedString(rejectCase.Body, "case", "id")
	if err != nil {
		return err
	}
	rejected, err := h.recordMeasurement(rejectCaseID, 26.5, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(rejected, http.StatusCreated); err != nil {
		return err
	}
	rejectedState, err := nestedString(rejected.Body, "case", "state")
	if err != nil {
		return err
	}
	if rejectedState != "recheck_required" {
		return fmt.Errorf("out-of-tolerance state = %q", rejectedState)
	}
	rejectedValue, err := nestedBool(rejected.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if rejectedValue {
		return errors.New("out-of-tolerance measurement was accepted")
	}
	return nil
}

func checkReopenVerificationCase() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.rejectedCase("MET-1301")
	if err != nil {
		return err
	}
	reopened, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "measurement setup was replaced",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(reopened, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(reopened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("reopened case state = %q", state)
	}
	measurements, err := nestedSlice(reopened.Body, "measurements")
	if err != nil {
		return err
	}
	if len(measurements) != 1 {
		return fmt.Errorf("reopen changed measurement history: %d entries", len(measurements))
	}

	second, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "duplicate request",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	events, err := h.request(http.MethodGet, "/cases/"+caseID+"/events", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(events, http.StatusOK); err != nil {
		return err
	}
	if !hasEvent(events.Body, "case_reopened") {
		return errors.New("reopen audit event is missing")
	}
	return nil
}

func checkSealVerification() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.readyCase("MET-1401")
	if err != nil {
		return err
	}
	type outcome struct {
		response apiResponse
		err      error
	}
	results := make(chan outcome, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			qualificationID, err := h.registerQualification(fmt.Sprintf("Reviewer-%d", index+1), validWindow())
			if err != nil {
				results <- outcome{err: err}
				return
			}
			response, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
				"reviewer":         fmt.Sprintf("Reviewer-%d", index+1),
				"summary":          "accepted measurement sealed for review",
				"qualification_id": qualificationID,
			})
			results <- outcome{response: response, err: err}
		}(index)
	}
	wait.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for item := range results {
		if item.err != nil {
			return item.err
		}
		switch item.response.Status {
		case http.StatusCreated:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			return fmt.Errorf("unexpected concurrent seal status %d: %s", item.response.Status, item.response.Raw)
		}
	}
	if successes != 1 || conflicts != 1 {
		return fmt.Errorf("concurrent seal outcomes: %d success, %d conflict", successes, conflicts)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(read.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "sealed" {
		return fmt.Errorf("sealed case state = %q", state)
	}
	sealID, err := nestedString(read.Body, "seal", "id")
	if err != nil {
		return err
	}
	if sealID == "" {
		return errors.New("evidence seal is missing")
	}
	return nil
}

// checkReviewerQualificationSeal covers the qualification -> seal chain:
// only an active, in-window qualification can seal; a rejected seal leaves no
// trace; the frozen basis survives later suspension/expiry; and a suspension
// racing a seal converges to exactly one outcome.
func checkReviewerQualificationSeal() error {
	h := newHarness()
	defer h.close()

	// A seal request without a qualification is rejected and leaves the
	// case untouched.
	caseID, err := h.readyCase("MET-1501")
	if err != nil {
		return err
	}
	missing, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
		"reviewer": "Reviewer-NoQual",
		"summary":  "seal without a qualification",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(missing, http.StatusBadRequest); err != nil {
		return err
	}
	if err := expectErrorCode(missing, "invalid_input"); err != nil {
		return err
	}

	// An expired qualification cannot seal and leaves no trace.
	expiredID, err := h.registerQualification("Reviewer-Expired", expiredWindow())
	if err != nil {
		return err
	}
	expired, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
		"reviewer":         "Reviewer-Expired",
		"summary":          "seal attempted after qualification expiry",
		"qualification_id": expiredID,
	})
	if err != nil {
		return err
	}
	if err := expectStatus(expired, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(expired, "state_conflict"); err != nil {
		return err
	}

	// A qualification registered for another reviewer does not authorize.
	mismatchID, err := h.registerQualification("Reviewer-SomeoneElse", validWindow())
	if err != nil {
		return err
	}
	mismatch, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
		"reviewer":         "Reviewer-Valid",
		"summary":          "seal attempted under someone else's qualification",
		"qualification_id": mismatchID,
	})
	if err != nil {
		return err
	}
	if err := expectStatus(mismatch, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(mismatch, "state_conflict"); err != nil {
		return err
	}

	// An unknown qualification id is rejected and leaves no trace.
	unknown, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
		"reviewer":         "Reviewer-Valid",
		"summary":          "seal with a missing qualification",
		"qualification_id": "qualification-9999",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(unknown, http.StatusNotFound); err != nil {
		return err
	}
	if err := expectErrorCode(unknown, "not_found"); err != nil {
		return err
	}

	// A not-yet-valid qualification cannot seal and leaves no trace.
	futureID, err := h.registerQualification("Reviewer-Future", futureWindow())
	if err != nil {
		return err
	}
	future, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
		"reviewer":         "Reviewer-Future",
		"summary":          "seal attempted before qualification starts",
		"qualification_id": futureID,
	})
	if err != nil {
		return err
	}
	if err := expectStatus(future, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(future, "state_conflict"); err != nil {
		return err
	}

	// A suspended qualification cannot seal and leaves no trace.
	suspendedID, err := h.registerQualification("Reviewer-Suspended", validWindow())
	if err != nil {
		return err
	}
	if err := h.suspendQualification(suspendedID, "reviewer temporarily suspended"); err != nil {
		return err
	}
	suspended, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
		"reviewer":         "Reviewer-Suspended",
		"summary":          "seal attempted while suspended",
		"qualification_id": suspendedID,
	})
	if err != nil {
		return err
	}
	if err := expectStatus(suspended, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(suspended, "state_conflict"); err != nil {
		return err
	}

	// None of the rejected attempts moved the case or created a seal.
	if err := h.assertCaseState(caseID, "review_ready"); err != nil {
		return err
	}

	// A valid reviewer seals successfully; the qualification basis is
	// frozen on the seal and still reads back after suspension.
	validID, err := h.registerQualification("Reviewer-Valid", validWindow())
	if err != nil {
		return err
	}
	sealed, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
		"reviewer":         "Reviewer-Valid",
		"summary":          "accepted measurement sealed under a valid qualification",
		"qualification_id": validID,
	})
	if err != nil {
		return err
	}
	if err := expectStatus(sealed, http.StatusCreated); err != nil {
		return err
	}
	basisReviewer, err := nestedString(sealed.Body, "authority", "reviewer")
	if err != nil {
		return err
	}
	if basisReviewer != "Reviewer-Valid" {
		return fmt.Errorf("authority reviewer = %q", basisReviewer)
	}
	basisStatus, err := nestedString(sealed.Body, "authority", "status")
	if err != nil {
		return err
	}
	if basisStatus != "active" {
		return fmt.Errorf("authority status = %q, expected active snapshot", basisStatus)
	}
	if err := h.suspendQualification(validID, "suspended after sealing"); err != nil {
		return err
	}
	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	if _, err := nestedString(read.Body, "authority", "qualification_id"); err != nil {
		return fmt.Errorf("sealed case no longer shows its qualification basis: %w", err)
	}
	readBasisStatus, err := nestedString(read.Body, "authority", "status")
	if err != nil {
		return err
	}
	if readBasisStatus != "active" {
		return fmt.Errorf("frozen basis was rewritten by suspension: status = %q", readBasisStatus)
	}
	readBasisID, err := nestedString(read.Body, "authority", "qualification_id")
	if err != nil {
		return err
	}
	if readBasisID != validID {
		return fmt.Errorf("frozen basis id = %q, expected %q", readBasisID, validID)
	}
	sealEvents, err := h.request(http.MethodGet, "/cases/"+caseID+"/events", nil)
	if err != nil {
		return err
	}
	if countEvents(sealEvents.Body, "case_sealed") != 1 {
		return errors.New("rejected seal attempts left audit events")
	}

	// The case is sealed once; a second valid reviewer cannot reseal.
	otherID, err := h.registerQualification("Reviewer-Other", validWindow())
	if err != nil {
		return err
	}
	again, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
		"reviewer":         "Reviewer-Other",
		"summary":          "repeat seal attempt",
		"qualification_id": otherID,
	})
	if err != nil {
		return err
	}
	if err := expectStatus(again, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(again, "seal_exists"); err != nil {
		return err
	}

	// Suspension and seal submitted together converge to one clear outcome:
	// either the seal commits first and keeps its frozen active basis (a later
	// suspension cannot rewrite it), or suspension commits first and the seal is
	// rejected with the case still reviewable and seal-free.
	return h.checkSuspensionSealRace("MET-1502")
}

func newHarness() *harness {
	server := httptest.NewServer(bootstrap.NewHandler())
	return &harness{
		baseURL: server.URL,
		client:  server.Client(),
		server:  server,
	}
}

func (h *harness) close() {
	h.server.Close()
}

func (h *harness) request(method, path string, body any) (apiResponse, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return apiResponse{}, err
		}
		reader = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, h.baseURL+path, reader)
	if err != nil {
		return apiResponse{}, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := h.client.Do(request)
	if err != nil {
		return apiResponse{}, err
	}
	defer response.Body.Close()
	rawBytes, err := io.ReadAll(response.Body)
	if err != nil {
		return apiResponse{}, err
	}
	result := apiResponse{Status: response.StatusCode, Raw: string(rawBytes)}
	if len(bytes.TrimSpace(rawBytes)) > 0 {
		if err := json.Unmarshal(rawBytes, &result.Body); err != nil {
			return apiResponse{}, fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return result, nil
}

func (h *harness) createDevice(tag string) (string, error) {
	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": tag,
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return nestedString(response.Body, "device", "id")
}

func (h *harness) openCase(deviceID string) (apiResponse, error) {
	response, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	})
	if err != nil {
		return apiResponse{}, err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return apiResponse{}, err
	}
	return response, nil
}

func (h *harness) recordMeasurement(caseID string, observed float64, technician string) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases/"+caseID+"/measurements", map[string]any{
		"observed":   observed,
		"technician": technician,
	})
}

func (h *harness) rejectedCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 26.5, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func (h *harness) readyCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

// checkSuspensionSealRace submits a suspension and a seal for the same
// qualification at the same time and requires one convergent result:
// exactly one side wins, and the persisted state matches the winners.
func (h *harness) checkSuspensionSealRace(tag string) error {
	caseID, err := h.readyCase(tag)
	if err != nil {
		return err
	}
	qualificationID, err := h.registerQualification("Reviewer-Race", validWindow())
	if err != nil {
		return err
	}
	type outcome struct {
		name     string
		response apiResponse
		err      error
	}
	results := make(chan outcome, 2)
	var wait sync.WaitGroup
	fire := func(name string, call func() (apiResponse, error)) {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response, err := call()
			results <- outcome{name: name, response: response, err: err}
		}()
	}
	fire("suspend", func() (apiResponse, error) {
		return h.request(http.MethodPost, "/qualifications/"+qualificationID+"/suspend", map[string]any{
			"reason": "suspension raced with seal",
		})
	})
	fire("seal", func() (apiResponse, error) {
		return h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
			"reviewer":         "Reviewer-Race",
			"summary":          "seal racing with suspension",
			"qualification_id": qualificationID,
		})
	})
	wait.Wait()
	close(results)

	statusByName := map[string]int{}
	for item := range results {
		if item.err != nil {
			return item.err
		}
		if item.response.Status != http.StatusOK &&
			item.response.Status != http.StatusCreated &&
			item.response.Status != http.StatusConflict {
			return fmt.Errorf("unexpected %s race status %d: %s", item.name, item.response.Status, item.response.Raw)
		}
		statusByName[item.name] = item.response.Status
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(read.Body, "case", "state")
	if err != nil {
		return err
	}

	switch {
	case statusByName["seal"] == http.StatusCreated:
		// Seal committed first: the suspension may still succeed, but it
		// can never reach into the completed seal. The persisted seal must
		// keep the active basis frozen at signing time, and not read back
		// as a seal signed by a suspended reviewer.
		if statusByName["suspend"] != http.StatusOK && statusByName["suspend"] != http.StatusConflict {
			return fmt.Errorf("seal won the race but suspension status = %d", statusByName["suspend"])
		}
		if state != "sealed" {
			return fmt.Errorf("seal reported success but case state = %q", state)
		}
		basis, err := nestedString(read.Body, "authority", "status")
		if err != nil {
			return err
		}
		if basis != "active" {
			return fmt.Errorf("seal basis = %q, expected the active snapshot frozen at sealing", basis)
		}
		basisReviewer, err := nestedString(read.Body, "authority", "reviewer")
		if err != nil {
			return err
		}
		if basisReviewer != "Reviewer-Race" {
			return fmt.Errorf("seal basis reviewer = %q", basisReviewer)
		}
	case statusByName["seal"] == http.StatusConflict && statusByName["suspend"] == http.StatusOK:
		// Suspension committed first: the seal is rejected and the
		// case stays reviewable with no seal.
		if state != "review_ready" {
			return fmt.Errorf("suspension won the race but case state = %q", state)
		}
		if _, hasSeal := read.Body["seal"]; hasSeal {
			return errors.New("rejected seal left a seal behind")
		}
	default:
		return fmt.Errorf("race did not converge: statuses = %v", statusByName)
	}
	return nil
}

func (h *harness) registerQualification(reviewer string, window [2]time.Time) (string, error) {
	response, err := h.request(http.MethodPost, "/qualifications", map[string]any{
		"reviewer":    reviewer,
		"scope":       "dimensional verification",
		"valid_from":  window[0].Format(time.RFC3339),
		"valid_until": window[1].Format(time.RFC3339),
	})
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return nestedString(response.Body, "qualification", "id")
}

func (h *harness) suspendQualification(id, reason string) error {
	response, err := h.request(http.MethodPost, "/qualifications/"+id+"/suspend", map[string]any{
		"reason": reason,
	})
	if err != nil {
		return err
	}
	return expectStatus(response, http.StatusOK)
}

func (h *harness) assertCaseState(caseID, expected string) error {
	response, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(response, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(response.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != expected {
		return fmt.Errorf("case state = %q, expected %q", state, expected)
	}
	if _, hasSeal := response.Body["seal"]; hasSeal {
		return errors.New("a rejected seal left an evidence seal behind")
	}
	return nil
}

func countEvents(root map[string]any, kind string) int {
	items, err := nestedSlice(root, "events")
	if err != nil {
		return 0
	}
	count := 0
	for _, item := range items {
		event, ok := item.(map[string]any)
		if ok && event["kind"] == kind {
			count++
		}
	}
	return count
}

func validWindow() [2]time.Time {
	now := time.Now().UTC()
	return [2]time.Time{now.Add(-time.Hour), now.Add(time.Hour)}
}

func expiredWindow() [2]time.Time {
	now := time.Now().UTC()
	return [2]time.Time{now.Add(-2 * time.Hour), now.Add(-time.Hour)}
}

func futureWindow() [2]time.Time {
	now := time.Now().UTC()
	return [2]time.Time{now.Add(time.Hour), now.Add(2 * time.Hour)}
}

func expectStatus(response apiResponse, expected int) error {
	if response.Status != expected {
		return fmt.Errorf("status = %d, expected %d: %s", response.Status, expected, response.Raw)
	}
	return nil
}

func expectErrorCode(response apiResponse, expected string) error {
	code, err := nestedString(response.Body, "error", "code")
	if err != nil {
		return err
	}
	if code != expected {
		return fmt.Errorf("error code = %q, expected %q", code, expected)
	}
	return nil
}

func nestedString(root map[string]any, path ...string) (string, error) {
	value, err := nestedValue(root, path...)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s is not a string", strings.Join(path, "."))
	}
	return text, nil
}

func nestedBool(root map[string]any, path ...any) (bool, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return false, err
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%v is not a boolean", path)
	}
	return flag, nil
}

func nestedSlice(root map[string]any, key string) ([]any, error) {
	value, ok := root[key]
	if !ok {
		return nil, fmt.Errorf("missing %s", key)
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s is not an array", key)
	}
	return items, nil
}

func hasEvent(root map[string]any, kind string) bool {
	items, err := nestedSlice(root, "events")
	if err != nil {
		return false
	}
	for _, item := range items {
		event, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if event["kind"] == kind {
			return true
		}
	}
	return false
}

func nestedValue(root map[string]any, path ...string) (any, error) {
	parts := make([]any, 0, len(path))
	for _, part := range path {
		parts = append(parts, part)
	}
	return nestedAny(root, parts...)
}

func nestedAny(root map[string]any, path ...any) (any, error) {
	var current any = root
	for _, part := range path {
		switch typed := part.(type) {
		case string:
			object, ok := current.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s is not an object", typed)
			}
			value, ok := object[typed]
			if !ok {
				return nil, fmt.Errorf("missing %s", typed)
			}
			current = value
		case int:
			items, ok := current.([]any)
			if !ok {
				return nil, fmt.Errorf("index %d is not inside an array", typed)
			}
			if typed < 0 || typed >= len(items) {
				return nil, fmt.Errorf("index %d is out of range", typed)
			}
			current = items[typed]
		default:
			return nil, fmt.Errorf("unsupported path element %T", part)
		}
	}
	return current, nil
}
