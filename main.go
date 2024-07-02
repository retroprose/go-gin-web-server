

/*
	This is where all the Session and Websockets
	code resides.  This could probably be optimized
	by using 

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"

	rather than

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
*/

//package server
package main

import (
	"os"
	"log"
	"encoding/binary"
	"unsafe"
	"fmt"
	"net/http"
	"strconv"
	"time"
	"math/rand"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

/*************************************
	
			Main Function

**************************************/

func main() {
	port := os.Getenv("PORT")
	//port := "5588"
	games := 10

	if port == "" {
		log.Fatal("$PORT must be set")
	}

	fmt.Printf("Listening on port %s", port)
	log.Fatal(Run(games, port))
}


/*************************************
	
			Main Run Server Function

**************************************/

// numSession, numPlayer, frameTime, inputSize
func Run(numSessions int, port string) error {

	s := serverType{
		router: mux.NewRouter(),
		numSessions: numSessions,
		sessions: make([]sessionType, numSessions),
	}

	for i := int(0); i < s.numSessions; i++  {
		s.sessions[i].initialize(i)
	}

	s.routes()

	return http.ListenAndServe(fmt.Sprintf(":%s", port), s.router)
}

/*************************************
	
			Server Type

**************************************/

type serverType struct {
	router *mux.Router
	numSessions int
	sessions []sessionType
}

func (s *serverType) routes() {
    s.router.HandleFunc("/session/{id}", s.handleWebSocket())
    s.router.HandleFunc("/", s.handleIndex())
}


func (s *serverType) handleIndex() http.HandlerFunc {
	// make thing
	return func(w http.ResponseWriter, r *http.Request) {
		// use thing
		fmt.Fprintf(w, "Index")        
    }
}


func (s *serverType) handleWebSocket() http.HandlerFunc {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true	
		},
	}
	return func(w http.ResponseWriter, r *http.Request) {
		id, id_err := strconv.Atoi(mux.Vars(r)["id"])
		if id_err != nil || id < 0 || id >= s.numSessions {
			fmt.Println("Could not parse id")
			return
		}

		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			fmt.Println("Could not upgrade")
			return
		}

		// find game number here
		s.sessions[id].handlerChan <- eventType{id: Join, socket: ws}
	}
}


/****************************************

		Event Type

*****************************************/


const (
	None uint8 = iota
	Join
	Leave
	Update
	InitData
	Broadcast
)

type eventType struct {
	id uint8
	socket *websocket.Conn
	playerId int8
	data []uint8
}

/****************************************

		Slot Type

*****************************************/

type frameType struct {
	buffer [MAX_INPUT]uint8
}

type slotType struct {
	active bool
	//missed int
	last int
	position int
	frames [MAX_BUFFER_INPUT]frameType
}


/****************************************

		Session Type

*****************************************/

const MAX_INPUT = 8
const MAX_PLAYERS = 32
const MAX_BUFFER_INPUT = 32
const FRAME_TICK = 33


type sessionType struct {
	id int
	frameTime time.Duration
	sockets [MAX_PLAYERS]*websocket.Conn

	handlerChan chan eventType
	loopChan chan bool
	
	framePosition int
	slots [MAX_PLAYERS]slotType
}


func (s *sessionType) initialize(id int) {
	*s = sessionType { 
		id: id,
		framePosition: 0,
		frameTime: FRAME_TICK * time.Millisecond,		
		handlerChan: make(chan eventType),
		loopChan: make(chan bool),
	}

	go s.eventHandler()
	go s.sessionLoop()
}


func (s *sessionType) eventHandler() {

	//fmt.Println("event Handler")
	
	var sysMsgBuf [1]uint8
	var sysInputBuf [MAX_INPUT]uint8
	var sysFrameBuf [MAX_PLAYERS*MAX_INPUT+12]uint8

	sysMsgBuf[0] = 0x00
	for i := 0; i < MAX_INPUT; i++ {
		sysInputBuf[i] = 0x00
	}

	for {
		event := <- s.handlerChan
		switch event.id {
		case Join:
			//fmt.Println("Joined")
			//fmt.Println("Joined Game")
			var playerId int8 = -1
			var i int8 = 0
			for playerId == -1 && i < MAX_PLAYERS {
				if (s.sockets[i] == nil) {
					playerId = i
				}
				i++;
			}

			if playerId != -1 {
				s.slots[playerId].active = true
				//s.slots[playerId].missed = 0
				s.slots[playerId].position = s.framePosition
				// zero out first frame and make it last good input
				s.slots[playerId].last = 0
				copy(s.slots[playerId].frames[0].buffer[:], sysInputBuf[:])

				s.sockets[playerId] = event.socket
				fmt.Println("Joined with playerId: ", playerId)
				
				// send a single byte
				sysMsgBuf[0] = *(*uint8)(unsafe.Pointer(&playerId))
				// send player id back to player
				s.sockets[playerId].WriteMessage(websocket.BinaryMessage, sysMsgBuf[:])

				go func (s *sessionType, playerId int8) {
					for {
						_, msg, err := s.sockets[playerId].ReadMessage()
						//fmt.Println("Msg: %i %s", msgType, msg)
						if err != nil {
							s.sockets[playerId].Close()
							s.handlerChan <- eventType{id: Leave, playerId: playerId}
							return
						}
						// convert message here
	
						//fmt.Printf("len 1\n")
						s.handlerChan <- eventType{
							id: Update, 
							playerId: playerId, 
							data: msg[:],
						}					
					}
				}(s, playerId)
			} else {
				// send message back to client saying too many players!
				event.socket.Close()
			}
		
		case Leave:
			fmt.Println("Leave with playerId: ", event.playerId)
			// zero out player's input
			s.slots[event.playerId].active = false
			s.sockets[event.playerId] = nil

		case Update:
			//if s.slots[event.playerId].missed > 0 {
			//	s.slots[event.playerId].missed -= 1
			//} else {
				lastPosition := s.framePosition - 1
				if lastPosition < 0 {
					lastPosition += MAX_BUFFER_INPUT
				}
				if s.slots[event.playerId].position != lastPosition {
					copy(s.slots[event.playerId].frames[s.slots[event.playerId].position].buffer[:], event.data[:])
					s.slots[event.playerId].last = s.slots[event.playerId].position
					s.slots[event.playerId].position += 1
					if s.slots[event.playerId].position >= MAX_BUFFER_INPUT {
						s.slots[event.playerId].position -= MAX_BUFFER_INPUT
					}
				}
			//}
			//fmt.Println("Input: ", event.data[:]);

		case Broadcast:
			var index int
			var seed uint32
			var active uint32
			var broken uint32
			seed = rand.Uint32()
			active = 0x00000000
			broken = 0x00000000
			for i := 0; i < MAX_PLAYERS; i++ {
				index = (i * MAX_INPUT) + 8
				if s.slots[i].active == true {
					active |= (0x00000001 << uint8(i))
				}
				if s.slots[i].position == s.framePosition {
					// copy in empty input
					//copy(sysFrameBuf[index:index+MAX_INPUT], sysInputBuf[:])
					// now we are going to send last valid input instead
					broken |= (0x00000001 << uint8(i))
					copy(sysFrameBuf[index:index+MAX_INPUT], s.slots[i].frames[s.slots[i].last].buffer[:])
					//s.slots[i].missed += 1
					s.slots[i].position += 1
					if s.slots[i].position >= MAX_BUFFER_INPUT {
						s.slots[i].position -= MAX_BUFFER_INPUT
					}
				} else {
					copy(sysFrameBuf[index:index+MAX_INPUT], s.slots[i].frames[s.framePosition].buffer[:])
				}
			}
			s.framePosition += 1
			if s.framePosition >= MAX_BUFFER_INPUT {
				s.framePosition -= MAX_BUFFER_INPUT
			}

			//s.active |= (0x0001 << uint8(playerId))
			//s.active &= ^(0x0001 << uint8(event.playerId))
			
			// send stuff out to everyone!
			//fmt.Println("sysFrameBuf: ", len(sysFrameBuf))
			binary.LittleEndian.PutUint32(sysFrameBuf[0:4], seed)
			binary.LittleEndian.PutUint32(sysFrameBuf[4:8], active)
			binary.LittleEndian.PutUint32(sysFrameBuf[8:12], broken)
			for i := 0; i < MAX_PLAYERS; i++ {
				if (s.sockets[i] != nil) {
					// set player id to i here
					s.sockets[i].WriteMessage(websocket.BinaryMessage, sysFrameBuf[:])
				}
			}
			s.loopChan <- true

		default:
			//fmt.Println("Default")

		}


	}


}


func (s *sessionType) sessionLoop() {

	for {
		// get time neede to run frame
		start := time.Now()

		// send frame off to all clients in this game
		s.handlerChan <- eventType{id: Broadcast}
		<- s.loopChan

		// frame locking code
		elapsed := time.Since(start)
		//fmt.Printf("Frame took %s\n", elapsed)
		
		if (elapsed > s.frameTime) {
			fmt.Printf("Failed to hold framerate for game %d   time: %d\n", s.id, elapsed)
		} else {
			time.Sleep(s.frameTime - elapsed)
		}
		//fmt.Printf("time %s\n", (33 * time.Millisecond) - elapsed)
	}


}





