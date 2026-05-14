class MockWebSocket {
  static OPEN = 1;
  readyState = MockWebSocket.OPEN;
  listeners: Record<string, Array<(event?: MessageEvent) => void>> = {};

  constructor() {
    setTimeout(() => {
      this.listeners.open?.forEach((listener) => listener());
      this.listeners.message?.forEach((listener) =>
        listener(
          new MessageEvent("message", {
            data: JSON.stringify({
              type: "snapshot",
              document: null,
              presence: [],
            }),
          }),
        ),
      );
    }, 0);
  }

  addEventListener(type: string, listener: (event?: MessageEvent) => void) {
    this.listeners[type] = [...(this.listeners[type] ?? []), listener];
  }

  removeEventListener(type: string, listener: (event?: MessageEvent) => void) {
    this.listeners[type] = (this.listeners[type] ?? []).filter((item) => item !== listener);
  }

  send() {}

  close() {
    this.listeners.close?.forEach((listener) => listener());
  }
}

class MockResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

const localStorageItems = new Map<string, string>();

Object.defineProperty(globalThis, "WebSocket", {
  writable: true,
  value: MockWebSocket,
});

Object.defineProperty(globalThis, "ResizeObserver", {
  writable: true,
  value: MockResizeObserver,
});

Object.defineProperty(globalThis, "localStorage", {
  writable: true,
  value: {
    getItem: (key: string) => localStorageItems.get(key) ?? null,
    setItem: (key: string, value: string) => {
      localStorageItems.set(key, value);
    },
    removeItem: (key: string) => {
      localStorageItems.delete(key);
    },
    clear: () => {
      localStorageItems.clear();
    },
  },
});
