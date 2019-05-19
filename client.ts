// start of javascript-based client.
// tsc --outDir ./_examples/browser client.ts

if (!("TextDecoder" in window) || !("TextEncoder" in window)) {
    throw new Error("this browser does not support Text Encoding/Decoding...");
}

var dec: TextDecoder = new TextDecoder("utf-8");
var enc: TextEncoder = new TextEncoder();

// type WSData = Uint8Array | string;

// var OnlyBinary: boolean = false;

// function toWSData(data: any): WSData {
//     if (data instanceof ArrayBuffer && !OnlyBinary) {
//         if (OnlyBinary) {
//             return new Uint8Array(data);
//         }
//         return dec.decode(data);
//     }

//     return data;
// }

type WSData = string;

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
    Body: WSData;
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

const messageSeparator = ';';
const validMessageSepCount = 7;

const trueString = "1";
const falseString = "0";

function serializeMessage(msg: Message): WSData {
    if (msg.IsNative && isEmpty(msg.wait)) {
        return msg.Body;
    }

    let isErrorString = falseString;
    let isNoOpString = falseString;
    let waitString = msg.wait || "";
    let body = msg.Body || "";

    if (msg.isError) {
        body = msg.Err;
        isErrorString = trueString;
    }

    if (msg.isNoOp) {
        isNoOpString = trueString
    }


    return [
        msg.wait,
        msg.Namespace,
        msg.Room,
        msg.Event,
        isErrorString,
        isNoOpString,
        body].join(messageSeparator);
}

// <wait>;
// <namespace>;
// <room>;
// <event>;
// <isError(0-1)>;
// <isNoOp(0-1)>;
// <body||error_message>
function deserializeMessage(data: WSData, allowNativeMessages: boolean): Message {
    var msg: Message = new Message();
    if (data.length == 0) {
        msg.isInvalid = true;
        return msg;
    }

    let dts = data.split(messageSeparator, validMessageSepCount);
    if (dts.length != validMessageSepCount) {
        if (!allowNativeMessages) {
            msg.isInvalid = true;
        } else {
            msg.Event = OnNativeMessage;
            msg.Body = data;
        }

        return msg;
    }

    msg.wait = dts[0];
    msg.Namespace = dts[1];
    msg.Room = dts[2];
    msg.Event = dts[3];
    msg.isError = dts[4] == trueString || false;
    msg.isNoOp = dts[5] == trueString || false;

    let body = dts[6];
    if (!isEmpty(body)) {
        if (msg.isError) {
            msg.Err = body;
        } else {
            msg.Body = body;
        }
    }

    msg.isInvalid = false;
    msg.IsForced = false;
    msg.IsLocal = false;
    msg.IsNative = (allowNativeMessages && msg.Event == OnNativeMessage) || false;
    // msg.SetBinary = false;
    return msg;
}

function isEmpty(s: string): boolean {
    if (s == undefined) {
        return true
    }

    return s.length == 0 || s == "";
}



const ErrInvalidPayload = "invalid payload";

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
    private allowNativeMessages: boolean; // TODO: when events done fill it on constructor.

    ID: string;

    queue: WSData[];

    // // listeners.
    // private errorListeners: (err:string)

    constructor(endpoint: string, protocols?: string[]) {
        if (!window["WebSocket"]) {
            return;
        }

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
            if (!this.isAcknowledged) {
                if (evt.data instanceof ArrayBuffer) {
                    let errorText = this.handleAck(new Uint8Array(evt.data));
                    if (errorText == undefined) {
                        this.isAcknowledged = true
                        this.handleQueue();
                    } else {
                        this.conn.close();
                        console.error(errorText);
                    }

                    return;
                }

                // let data = toWSData(evt.data)
                this.queue.push(evt.data);
                return;
            }

            // let data = toWSData(evt.data); 

            console.log(evt.data);

            let err = this.handleMessage(evt.data);
        });
    }

    private handleAck(data: Uint8Array): string {
        let typ = data[0];
        switch (typ) {
            case ackIDBinary:
                let id = dec.decode(data.slice(1));
                this.ID = id;
                console.info("SET ID: ", id);
                break;
            case ackNotOKBinary:
                let errorText = dec.decode(data.slice(1));
                return errorText;
            default:
                return "";
        }
    }

    private handleQueue(): void {
        if (this.queue == undefined || this.queue.length == 0) {
            return;
        }

        this.queue.forEach((item, index) => {
            this.queue.splice(index, 1);
            this.handleMessage(item);
        });
    }

    private handleMessage(data: WSData): string {
        let msg = deserializeMessage(data, this.allowNativeMessages)
        if (msg.isInvalid) {
            return ErrInvalidPayload
        }

        console.info(msg);

        // TODO: ...

        // it's a native websocket message
        // this.handleNativeMessage(data);
    }

    private handleNativeMessage(data: WSData): void {
        // console.log(data);
    }
}

