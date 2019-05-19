// start of javascript-based client.
// tsc --outDir ./_examples/browser client.ts

const OnNamespaceConnect = "_OnNamespaceConnect";
const OnNamespaceConnected = "_OnNamespaceConnected";
const OnNamespaceDisconnect = "_OnNamespaceDisconnect";

const OnRoomJoin = "_OnRoomJoin";
const OnRoomJoined = "_OnRoomJoined";
const OnRoomLeave = "_OnRoomLeave";
const OnRoomLeft = "_OnRoomLeft";

const OnAnyEvent = "_OnAnyEvent";
const OnNativeMessage = "_OnNativeMessage";

// see `handleAck`.
const ackIDBinary = 2;// comes from server to client after ackBinary and ready as a prefix, the rest message is the conn's ID.
const ackNotOKBinary = 4; // comes from server to client if `Server#OnConnected` errored as a prefix, the rest message is the error text.

function IsSystemEvent(event: string): boolean {
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

class Message {
    wait: string;

    Namespace: string;
    Room: string;
    Event: string;
    Body: Int8Array;
    Err: string;

    isError: boolean;
    isNoOp: boolean;

    isInvalid: boolean;

    from: string;

    IsForced: boolean;
    IsLocal: boolean;

    IsNative: boolean;

    isConnect(): boolean {
        return this.Event == OnNamespaceConnect
    }

    isDisconnect(): boolean {
        return this.Event == OnNamespaceDisconnect
    }


    isRoomJoin(): boolean {
        return this.Event == OnRoomJoin
    }


    isRoomLeft(): boolean {
        return this.Event == OnRoomLeft
    }
}


// interface Events {
//     "error": Event;
//     "message": MessageEvent;
//     "open": Event;
// }

class Ws {
    private conn: WebSocket;
    private dec: TextDecoder;
    private enc: TextEncoder;

    private isAcknowledged: boolean;

    ID: string;

    // // listeners.
    // private errorListeners: (err:string)

    constructor(endpoint: string, protocols?: string[]) {
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

        this.conn.onerror = ((evt: Event) => {
            console.error("WebSocket error observed:", event);
        });

        this.conn.onopen = ((evt: Event): any => {
            console.log("WebSocket connected.");
            let b = new Uint8Array(1)
            b[0] = 1;
            this.conn.send(b.buffer);
            return null;
        });

        this.conn.onclose = ((evt: Event): any => {
            console.log("WebSocket disconnected.");
            return null;
        });

        this.conn.onmessage = ((evt: MessageEvent) => {
            console.log("WebSocket On Message.");

            if (evt.data instanceof ArrayBuffer) {
                if (!this.isAcknowledged) {
                    let errorText = this.handleAck(new Uint8Array(evt.data));
                    if (errorText == undefined) {
                        this.isAcknowledged = true
                    } else {
                        this.conn.close();
                        console.error(errorText);
                    }
                }
            }

            console.log(evt.data);

            this.handleMessage(evt.data);
        });
    }

    private handleAck(data: Uint8Array): string {
        let typ = data[0];
        switch (typ) {
            case ackIDBinary:
                let id = this.dec.decode(data.slice(1));
                this.ID = id;
                break;
            case ackNotOKBinary:
                let errorText = this.dec.decode(data.slice(1));
                return errorText;
            default:
                return "";
        }
    }

    private handleMessage(data: Int8Array): void {

        // it's a native websocket message
        this.handleNativeMessage(data);
    }

    private handleNativeMessage(data: Int8Array): void {
        // console.log(data);
    }
}