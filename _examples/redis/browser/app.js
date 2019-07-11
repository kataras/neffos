const neffos = require('neffos.js');

var scheme = document.location.protocol == "https:" ? "wss" : "ws";
var port = document.location.port ? ":" + document.location.port : "";
var wsURL = scheme + "://" + document.location.hostname + port + "/echo";

var outputTxt = document.getElementById("output");

function addMessage(msg) {
    outputTxt.innerHTML += msg + "\n";
}

function handleError(reason) {
    console.log(reason);
    window.alert(reason);
}

class UserMessage {
    constructor(from, to, text) {
        this.from = from;
        this.to = to; // can be empty.
        this.text = text;
    }
}


async function handleNamespaceConnectedConn(nsConn) {
    let roomToJoin = prompt("Please specify a room to join, i.e room1: ");
    nsConn.joinRoom(roomToJoin);

    let inputToTxt = document.getElementById("inputTo");
    let inputTxt = document.getElementById("input");
    let sendBtn = document.getElementById("sendBtn");


    sendBtn.disabled = false;
    sendBtn.onclick = function () {
        const input = inputTxt.value;
        inputTxt.value = "";
        const toID = inputToTxt.value;

        switch (input) {
            case "leave":
                nsConn.room(roomToJoin).leave();
                roomToJoin = "";
                // or room := nsConn.joinRoom.... room.leave(); 
                break;
            default:
                const userMsg = new UserMessage(nsConn.conn.ID, toID, input);

                if (roomToJoin !== "") {
                    nsConn.room(roomToJoin).emit("chat", neffos.marshal(userMsg))
                } else {
                    nsConn.emit("chat", neffos.marshal(userMsg));
                }

                addMessage("Me: " + input);
        }
    };
}

async function runExample() {
    // You can omit the "default" and simply define only Events, the namespace will be an empty string"",
    // however if you decide to make any changes on this example make sure the changes are reflecting inside the ../server.go file as well.
    try {
        const username = prompt("Please specify a username: ");

        const conn = await neffos.dial(wsURL, {
            default: { // "default" namespace.
                _OnNamespaceConnected: function (nsConn, msg) {
                    addMessage("connected to namespace: " + msg.Namespace);
                    handleNamespaceConnectedConn(nsConn);
                },
                _OnNamespaceDisconnect: function (nsConn, msg) {
                    addMessage("disconnected from namespace: " + msg.Namespace);
                },
                _OnRoomJoined: function (nsConn, msg) {
                    addMessage("joined to room: " + msg.Room);
                },
                _OnRoomLeft: function (nsConn, msg) {
                    addMessage("left from room: " + msg.Room);
                },
                notify: function (nsConn, msg) {
                    addMessage(msg.Body);
                },
                chat: function (nsConn, msg) { // "chat" event.
                    const userMsg = msg.unmarshal()
                    if (userMsg.to && userMsg.to !== "") {
                        userMsg.from = "private message: " + userMsg.from;
                    }

                    if (msg.Room !== "") {
                        userMsg.from = msg.Room + ": " + userMsg.from;
                    }
                    addMessage(userMsg.from + ": " + userMsg.text);
                }
            }
        }, {
                headers: { 'X-Username': username }
            });

        conn.connect("default");

    } catch (err) {
        handleError(err);
    }
}

runExample();