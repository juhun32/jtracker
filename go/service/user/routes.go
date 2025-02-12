package user

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"context"

	"github.com/juhun32/jtracker-backend/service/auth"
	"github.com/juhun32/jtracker-backend/utils"

	"cloud.google.com/go/firestore"
	"github.com/gorilla/mux"
	amqp "github.com/rabbitmq/amqp091-go"
	"google.golang.org/api/iterator"
)

type AddApplicationRequest struct {
	Role        string `json:"role"`
	Company     string `json:"company"`
	Location    string `json:"location"`
	AppliedDate string `json:"appliedDate"`
	Status      string `json:"status"`
	Link        string `json:"link"`
}

type Application struct {
	ID          string `json:"id"`
	Role        string `json:"role"`
	Company     string `json:"company"`
	Location    string `json:"location"`
	AppliedDate string `json:"appliedDate"`
	Status      string `json:"status"`
	Link        string `json:"link"`
}

type DeleteApplicationRequest struct {
	ID string `json:"id"`
}

type EditStatusApplicationRequest struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// edit application does not include status because status is edited separately
type EditApplicationRequest struct {
	ID          string `json:"id"`
	Role        string `json:"role"`
	Company     string `json:"company"`
	Location    string `json:"location"`
	AppliedDate string `json:"appliedDate"`
	Link        string `json:"link"`
}

type Handler struct {
	firestoreClient *firestore.Client
	rabbitCh        *amqp.Channel
	rabbitQ         amqp.Queue
}

func NewHandler(firestoreClient *firestore.Client,
	rabbitCh *amqp.Channel,
	rabbitQ amqp.Queue,
) *Handler {
	return &Handler{
		firestoreClient: firestoreClient,
		rabbitCh:        rabbitCh,
		rabbitQ:         rabbitQ,
	}
}

func (h *Handler) RegisterRoutes(router *mux.Router) {
	router.HandleFunc("/user/dashboard", h.Dashboard).Methods("GET").Name("dashboard")
	router.HandleFunc("/user/profile", h.Profile).Methods("GET").Name("profile")
	router.HandleFunc("/user/addApplication", h.AddApplication).Methods("POST").Name("addApplication")
	router.HandleFunc("/user/deleteApplication", h.DeleteApplication).Methods("POST").Name("deleteApplication")
	router.HandleFunc("/user/editStatus", h.EditStatus).Methods("POST").Name("editStatus")
	router.HandleFunc("/user/editApplication", h.EditApplication).Methods("POST").Name("editApplication")
	router.HandleFunc("/user/deleteUser", h.DeleteUser).Methods("POST").Name("deleteUser")
}

func (h *Handler) AddApplication(w http.ResponseWriter, r *http.Request) {
	log.Println("[*] AddApplication [*]")
	log.Println("-----------------")

	user, err := auth.IsAuthenticated(r)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// for user's email (unique ID), add application and assign unique jobID
	email := user.Email

	log.Println("User authenticated")

	// extract json from request body
	var addApplicationRequest AddApplicationRequest
	err = json.NewDecoder(r.Body).Decode(&addApplicationRequest)
	if err != nil {
		http.Error(w, "Error decoding request body", http.StatusBadRequest)
		return
	}

	// add application to Firestore using users/{email} where jobs is a document within the user's collection
	doc, _, err := h.firestoreClient.Collection("users").Doc(email).Collection("applications").Add(r.Context(), map[string]interface{}{
		"role":        addApplicationRequest.Role,
		"company":     addApplicationRequest.Company,
		"location":    addApplicationRequest.Location,
		"appliedDate": addApplicationRequest.AppliedDate,
		"status":      addApplicationRequest.Status,
		"link":        addApplicationRequest.Link,
	})
	if err != nil {
		fmt.Printf("Error adding application: %v\n", err)
		http.Error(w, "Error adding application", http.StatusInternalServerError)
		return
	}

	log.Println("Application added")

	// Read the current applicationsCount from the user's document
	userDoc, err := h.firestoreClient.Collection("users").Doc(email).Get(r.Context())
	if err != nil {
		fmt.Printf("Error retrieving user data: %v\n", err)
		http.Error(w, "Error retrieving user data", http.StatusInternalServerError)
		return
	}

	applicationsCount, ok := userDoc.Data()["applicationsCount"].(int64)
	if !ok {
		fmt.Printf("Error: applicationsCount is not an integer")
		http.Error(w, "Error retrieving applications count", http.StatusInternalServerError)
		return
	}

	// Increment the applicationsCount by 1
	applicationsCount++

	// Update the applicationsCount in the user's document
	_, err = h.firestoreClient.Collection("users").Doc(email).Update(r.Context(), []firestore.Update{
		{
			Path:  "applicationsCount",
			Value: applicationsCount,
		},
	})
	if err != nil {
		fmt.Printf("Error updating applications count: %v\n", err)
		http.Error(w, "Error updating applications count", http.StatusInternalServerError)
		return
	}

	log.Println("Applications count updated, added by 1")

	w.WriteHeader(http.StatusCreated)

	// for now, just send msg to rabbitmq (later, we will ensure that the application is indexed)
	message := map[string]string{
		"operation":   "add",
		"email":       email,
		"appliedDate": addApplicationRequest.AppliedDate,
		"company":     addApplicationRequest.Company,
		"link":        addApplicationRequest.Link,
		"location":    addApplicationRequest.Location,
		"role":        addApplicationRequest.Role,
		"status":      addApplicationRequest.Status,
		"objectID":    doc.ID,
	}

	messageBody, err := json.Marshal(message)
	if err != nil {
		fmt.Printf("Error marshaling message: %v\n", err)
		return
	}

	err = utils.PublishWithRetry(h.rabbitCh, "", h.rabbitQ.Name, false, false, amqp.Publishing{
		ContentType: "text/plain",
		Body:        messageBody,
	})
	if err != nil {
		fmt.Printf("Error publishing message after retries: %v\n", err)
	} else {
		log.Println("Message published")
		log.Println("-----------------")
	}
}

func (h *Handler) Profile(w http.ResponseWriter, r *http.Request) {
	log.Println("[*] Profile [*]")
	log.Println("-----------------")

	user, err := auth.IsAuthenticated(r)

	if err != nil {
		fmt.Printf("Error: %v\n", err)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	log.Println("User authenticated")

	// get user's email
	email := user.Email

	// get user's applications count
	doc, err := h.firestoreClient.Collection("users").Doc(email).Get(r.Context())
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		http.Error(w, "Error retrieving user data", http.StatusInternalServerError)
		return
	}
	applicationsCount := doc.Data()["applicationsCount"].(int64)
	log.Println("Applications count retrieved")
	log.Println("-----------------")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"email":             email,
		"applicationsCount": applicationsCount,
	})
}

// current implementation is TEMPORARY!!!!
// actual implementation doesn't query Firestore, it queries Algolia
// Firestore is just a backup in case we want to switch to a diff search engine
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	log.Println("[*] Dashboard [*]")
	log.Println("-----------------")

	user, err := auth.IsAuthenticated(r)

	if err != nil {
		fmt.Printf("Error: %v\n", err)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	log.Println("User authenticated")

	// the actual implementation of this will use the user object from auth.IsAuthenticated
	// TEMP: query firestore for user's applications
	// TODO: query Algolia for user's applications instead
	email := user.Email

	iter := h.firestoreClient.Collection("users").Doc(email).Collection("applications").Documents(r.Context())
	var applications []Application

	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			http.Error(w, "Error querying Firestore", http.StatusInternalServerError)
			return
		}

		application := doc.Data()
		applications = append(applications, Application{
			ID:          doc.Ref.ID,
			Role:        utils.GetString(application, "role"),
			Company:     utils.GetString(application, "company"),
			Location:    utils.GetString(application, "location"),
			AppliedDate: utils.GetString(application, "appliedDate"),
			Status:      utils.GetString(application, "status"),
			Link:        utils.GetString(application, "link"), // link might be nil so getString
		})
	}

	log.Println("Applications retrieved")
	log.Println("-----------------")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(applications)
}

func (h *Handler) DeleteApplication(w http.ResponseWriter, r *http.Request) {
	log.Println("[*] DeleteApplication [*]")
	log.Println("-----------------")

	user, err := auth.IsAuthenticated(r)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	email := user.Email

	log.Println("User authenticated")

	// extract json from request body
	var deleteApplicationRequest DeleteApplicationRequest
	err = json.NewDecoder(r.Body).Decode(&deleteApplicationRequest)
	if err != nil {
		http.Error(w, "Error decoding request body", http.StatusBadRequest)
		return
	}

	applicationID := deleteApplicationRequest.ID

	// delete application from Firestore
	_, err = h.firestoreClient.Collection("users").Doc(email).Collection("applications").Doc(applicationID).Delete(r.Context())
	if err != nil {
		fmt.Printf("Error deleting application: %v\n", err)
		http.Error(w, "Error deleting application", http.StatusInternalServerError)
		return
	}

	log.Println("Application deleted")

	// Read the current applicationsCount from the user's document
	userDoc, err := h.firestoreClient.Collection("users").Doc(email).Get(r.Context())
	if err != nil {
		fmt.Printf("Error retrieving user data: %v\n", err)
		http.Error(w, "Error retrieving user data", http.StatusInternalServerError)
		return
	}

	applicationsCount, ok := userDoc.Data()["applicationsCount"].(int64)
	if !ok {
		fmt.Printf("Error: applicationsCount is not an integer")
		http.Error(w, "Error retrieving applications count", http.StatusInternalServerError)
		return
	}

	// Decrement the applicationsCount by 1 if it's greater than 0
	if applicationsCount > 0 {
		applicationsCount--
	}

	// Update the applicationsCount in the user's document
	_, err = h.firestoreClient.Collection("users").Doc(email).Update(r.Context(), []firestore.Update{
		{
			Path:  "applicationsCount",
			Value: applicationsCount,
		},
	})
	if err != nil {
		fmt.Printf("Error updating applications count: %v\n", err)
		http.Error(w, "Error updating applications count", http.StatusInternalServerError)
		return
	}

	log.Println("Applications count updated, subtracted by 1")

	w.WriteHeader(http.StatusOK)

	message := map[string]string{
		"operation": "delete",
		"email":     email,
		"objectID":  applicationID,
	}

	messageBody, err := json.Marshal(message)
	if err != nil {
		fmt.Printf("Error marshaling message: %v\n", err)
		return
	}

	err = utils.PublishWithRetry(h.rabbitCh, "", h.rabbitQ.Name, false, false, amqp.Publishing{
		ContentType: "text/plain",
		Body:        messageBody,
	})
	if err != nil {
		fmt.Printf("Error publishing message after retries: %v\n", err)
	} else {
		log.Println("Message published")
		log.Println("-----------------")
	}
}

func (h *Handler) EditStatus(w http.ResponseWriter, r *http.Request) {
	log.Println("[*] EditStatus [*]")
	log.Println("-----------------")

	user, err := auth.IsAuthenticated(r)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	email := user.Email

	log.Println("User authenticated")

	// extract json from request body
	var editStatusApplicationRequest EditStatusApplicationRequest
	err = json.NewDecoder(r.Body).Decode(&editStatusApplicationRequest)
	if err != nil {
		http.Error(w, "Error decoding request body", http.StatusBadRequest)
		return
	}

	applicationID := editStatusApplicationRequest.ID

	// edit application in Firestore
	_, err = h.firestoreClient.Collection("users").Doc(email).Collection("applications").Doc(applicationID).Update(r.Context(), []firestore.Update{
		{
			Path:  "status",
			Value: editStatusApplicationRequest.Status,
		},
	})
	if err != nil {
		fmt.Printf("Error adding application: %v\n", err)
		http.Error(w, "Error adding application", http.StatusInternalServerError)
		return
	}

	log.Println("Application status edited")

	w.WriteHeader(http.StatusCreated)

	message := map[string]string{
		"operation": "edit",
		"email":     email,
		"objectID":  applicationID,
		"status":    editStatusApplicationRequest.Status,
	}

	messageBody, err := json.Marshal(message)
	if err != nil {
		fmt.Printf("Error marshaling message: %v\n", err)
		return
	}

	err = utils.PublishWithRetry(h.rabbitCh, "", h.rabbitQ.Name, false, false, amqp.Publishing{
		ContentType: "text/plain",
		Body:        messageBody,
	})
	if err != nil {
		fmt.Printf("Error publishing message after retries: %v\n", err)
	} else {
		log.Println("Message published")
		log.Println("-----------------")
	}
}

func (h *Handler) EditApplication(w http.ResponseWriter, r *http.Request) {
	log.Println("[*] EditApplication [*]")
	log.Println("-----------------")

	user, err := auth.IsAuthenticated(r)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	email := user.Email

	log.Println("User authenticated")

	// extract json from request body
	var addApplicationRequest EditApplicationRequest
	err = json.NewDecoder(r.Body).Decode(&addApplicationRequest)
	if err != nil {
		http.Error(w, "Error decoding request body", http.StatusBadRequest)
		return
	}

	applicationID := addApplicationRequest.ID

	// frontend will send all fields, so we need to update all fields
	_, err = h.firestoreClient.Collection("users").Doc(email).Collection("applications").Doc(applicationID).Update(r.Context(), []firestore.Update{
		{
			Path:  "role",
			Value: addApplicationRequest.Role,
		},
		{
			Path:  "company",
			Value: addApplicationRequest.Company,
		},
		{
			Path:  "location",
			Value: addApplicationRequest.Location,
		},
		{
			Path:  "link",
			Value: addApplicationRequest.Link,
		},
		{
			Path:  "appliedDate",
			Value: addApplicationRequest.AppliedDate,
		},
	})
	if err != nil {
		fmt.Printf("Error editing application: %v\n", err)
		http.Error(w, "Error editing application", http.StatusInternalServerError)
		return
	}

	log.Println("Application edited")

	w.WriteHeader(http.StatusOK)

	message := map[string]string{
		"operation":   "edit",
		"email":       email,
		"appliedDate": addApplicationRequest.AppliedDate,
		"company":     addApplicationRequest.Company,
		"link":        addApplicationRequest.Link,
		"location":    addApplicationRequest.Location,
		"role":        addApplicationRequest.Role,
		"objectID":    applicationID,
	}

	messageBody, err := json.Marshal(message)
	if err != nil {
		fmt.Printf("Error marshaling message: %v\n", err)
		return
	}

	err = utils.PublishWithRetry(h.rabbitCh, "", h.rabbitQ.Name, false, false, amqp.Publishing{
		ContentType: "text/plain",
		Body:        messageBody,
	})
	if err != nil {
		fmt.Printf("Error publishing message after retries: %v\n", err)
	} else {
		log.Println("Message published")
		log.Println("-----------------")
	}
}

func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	log.Println("[*] DeleteUser [*]")
	log.Println("-----------------")

	user, err := auth.IsAuthenticated(r)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	email := user.Email

	log.Println("User authenticated")

	// delete user in Firestore
	err = h.deleteUserFromFirestore(email, 10)
	if err != nil {
		fmt.Printf("Error deleting user: %v\n", err)
		http.Error(w, "Error deleting user", http.StatusInternalServerError)
		return
	}

	log.Println("User deleted")

	w.WriteHeader(http.StatusOK)

	// send to algolia to delete all applications associated with this user
	message := map[string]string{
		"operation": "userDelete",
		"email":     email,
	}

	messageBody, err := json.Marshal(message)
	if err != nil {
		fmt.Printf("Error marshaling message: %v\n", err)
		return
	}

	err = utils.PublishWithRetry(h.rabbitCh, "", h.rabbitQ.Name, false, false, amqp.Publishing{
		ContentType: "text/plain",
		Body:        messageBody,
	})
	if err != nil {
		fmt.Printf("Error publishing message after retries: %v\n", err)
	} else {
		log.Println("Message published")
		log.Println("-----------------")
	}
}

// Firestore does not delete subcollections automatically
// so, delete all documents in users/{email}/applications
// then, delete users/{email}
func (h *Handler) deleteUserFromFirestore(email string, batchSize int) error {
	ctx := context.Background()

	// delete subcollection FIRST (just applications)
	applicationsCollection := h.firestoreClient.Collection("users").Doc(email).Collection("applications")
	bulkWriter := h.firestoreClient.BulkWriter(ctx)

	// for each batch... 
	for {
		iter := applicationsCollection.Limit(batchSize).Documents(ctx)
		numDeleted := 0

		// for each document...
		for {
			doc, err := iter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				return fmt.Errorf("Failed to iterate: %v", err)
			}

			bulkWriter.Delete(doc.Ref)
			numDeleted++
		}

		if numDeleted == 0 {
			bulkWriter.End()
			break
		}

		bulkWriter.Flush()
	}

	fmt.Println("Applications subcollection deleted for user", email)

	// delete user document
	_, err := h.firestoreClient.Collection("users").Doc(email).Delete(ctx)
	if err != nil {
		return fmt.Errorf("Failed to delete user document: %v", err)
	}

	fmt.Println("User document deleted for user", email)

	return nil
}