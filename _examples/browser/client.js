// start of javascript-based client.
// tsc --outDir ./_examples/browser client.ts
var OnNamespaceConnect = "_OnNamespaceConnect";
var OnNamespaceConnected = "_OnNamespaceConnected";
var OnNamespaceDisconnect = "_OnNamespaceDisconnect";
var OnRoomJoin = "_OnRoomJoin";
var OnRoomJoined = "_OnRoomJoined";
var OnRoomLeave = "_OnRoomLeave";
var OnRoomLeft = "_OnRoomLeft";
var OnAnyEvent = "_OnAnyEvent";
var OnNativeMessage = "_OnNativeMessage";
// see `handleAck`.
var ackIDBinary = 2; // comes from server to client after ackBinary and ready as a prefix, the rest message is the conn's ID.
var ackNotOKBinary = 4; // comes from server to client if `Server#OnConnected` errored as a prefix, the rest message is the error text.
function IsSystemEvent(event) {
    switch (event) {
        case OnNamespaceConnect:
        case OnNamespaceConnected:
        case OnNamespaceDisconnect:
        case OnRoomJoin:
        case OnRoomJoined:
        case OnRoomLeave:
        case OnRoomLeft:
            return true;
        default:
            return false;
    }
}
var Message = /** @class */ (function () {
    function Message() {
    }
    Message.prototype.isConnect = function () {
        return this.Event == OnNamespaceConnect;
    };
    Message.prototype.isDisconnect = function () {
        return this.Event == OnNamespaceDisconnect;
    };
    Message.prototype.isRoomJoin = function () {
        return this.Event == OnRoomJoin;
    };
    Message.prototype.isRoomLeft = function () {
        return this.Event == OnRoomLeft;
    };
    return Message;
}());
// interface Events {
//     "error": Event;
//     "message": MessageEvent;
//     "open": Event;
// }
var Ws = /** @class */ (function () {
    // // listeners.
    // private errorListeners: (err:string)
    function Ws(endpoint, protocols) {
        var _this = this;
        if (!window["WebSocket"]) {
            return;
        }
        if (!("TextDecoder" in window)) {
            console.log("this browser does not support TextDecoder...");
        }
        if (!("TextEncoder" in window)) {
            // Uint8Array.from(str, c => c.codePointAt(0));
            console.log("this browser does not support TextEncoder...");
        }
        this.dec = new TextDecoder("utf-8");
        this.enc = new TextEncoder();
        if (endpoint.indexOf("ws") == -1) {
            endpoint = "ws://" + endpoint;
        }
        this.conn = new WebSocket(endpoint, protocols);
        this.conn.binaryType = "arraybuffer";
        this.conn.onerror = (function (evt) {
            console.error("WebSocket error observed:", event);
        });
        this.conn.onopen = (function (evt) {
            console.log("WebSocket connected.");
            var b = new Uint8Array(1);
            b[0] = 1;
            _this.conn.send(b.buffer);
            return null;
        });
        this.conn.onclose = (function (evt) {
            console.log("WebSocket disconnected.");
            return null;
        });
        this.conn.onmessage = (function (evt) {
            console.log("WebSocket On Message.");
            if (evt.data instanceof ArrayBuffer) {
                if (!_this.isAcknowledged) {
                    var errorText = _this.handleAck(new Uint8Array(evt.data));
                    if (errorText == undefined) {
                        _this.isAcknowledged = true;
                    }
                    else {
                        _this.conn.close();
                        console.error(errorText);
                    }
                }
            }
            console.log(evt.data);
            _this.handleMessage(evt.data);
        });
    }
    Ws.prototype.handleAck = function (data) {
        var typ = data[0];
        switch (typ) {
            case ackIDBinary:
                var id = this.dec.decode(data.slice(1));
                this.ID = id;
                break;
            case ackNotOKBinary:
                var errorText = this.dec.decode(data.slice(1));
                return errorText;
            default:
                return "";
        }
    };
    Ws.prototype.handleMessage = function (data) {
        // it's a native websocket message
        this.handleNativeMessage(data);
    };
    Ws.prototype.handleNativeMessage = function (data) {
        // console.log(data);
    };
    return Ws;
}());
