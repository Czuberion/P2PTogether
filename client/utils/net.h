#pragma once

#include <QtNetwork/QTcpServer>

/*!
 * \brief Returns an unused TCP port bound to 127.0.0.1.
 *
 * Opens a QTcpServer on 0 (port 0 = “any free port”), immediately retrieves the
 * assigned port, then closes the server.  Guaranteed not to conflict on the
 * current machine at the moment of the call.
 *
 * \throws std::runtime_error if no port could be obtained.
 */
inline quint16 findFreeTcpPort() {
    QTcpServer probe;
    // Bind to loop‑back only; QAbstractSocket::AnyIPv4 instead of dual‑stack
    if (!probe.listen(QHostAddress::LocalHost, /*port = */ 0))
        throw std::runtime_error(
            ("findFreeTcpPort(): " + probe.errorString()).toStdString());

    quint16 port = probe.serverPort();
    probe.close();
    return port;
}
