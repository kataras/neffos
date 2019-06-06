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
    constructor(from, text) {
        this.from = from;
        this.text = text;
    }
}

function handleNamespaceConnectedConn(nsConn) {
    var username = prompt("Please specify a username: ");

    let inputTxt = document.getElementById("input");
    let sendBtn = document.getElementById("sendBtn");

    sendBtn.disabled = false;
    sendBtn.onclick = function () {
        const input = inputTxt.value;
        inputTxt.value = "";
        const userMsg = new UserMessage(username,input);
        nsConn.emit("chat", neffos.marshal(userMsg));
        addMessage("Me: " + input);
    };
}

async function runExample() {
    // You can omit the "default" and simply define only Events, the namespace will be an empty string"",
    // however if you decide to make any changes on this example make sure the changes are reflecting inside the ../server.go file as well.
    try {
        const conn = await neffos.dial(wsURL, {
            default: { // "default" namespace.
                _OnNamespaceConnected: function (nsConn, msg) {
                    addMessage("connected to namespace: " + msg.Namespace);
                    handleNamespaceConnectedConn(nsConn);
                },
                _OnNamespaceDisconnect: function (nsConn, msg) {
                    addMessage("disconnected from namespace: " + msg.Namespace);
                },
                chat: function (nsConn, msg) { // "chat" event.
                    const userMsg = msg.unmarshal()
                    addMessage(userMsg.from + ": " + userMsg.text);
                }
            }
        });

        conn.connect("default");

    } catch (err) {
        handleError(err);
    }
}

runExample();