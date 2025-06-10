#pragma once

#include <QMap>
#include <QObject>
#include <QReadWriteLock> // For thread-safe access if needed from multiple threads
#include <QString>
#include <QVector>

#include "client.pb.h"   // For ServerMsg and its payloads
#include "p2p/role.pb.h" // For RoleDefinition, PeerRoleAssignment etc.

namespace P2P {
namespace Roles {

// Forward declaration if RoleStore is used by other client classes via pointers
// class RoleStore;

class RoleStore : public QObject {
    Q_OBJECT

public:
    explicit RoleStore(QObject* parent = nullptr);
    ~RoleStore();

    // --- Methods to update store from server messages ---
    Q_INVOKABLE void processRoleDefinitionsUpdate(
        const client::p2p::RoleDefinitionsUpdate& update);
    Q_INVOKABLE void processPeerRoleAssignment(
        const client::p2p::PeerRoleAssignment& assignment);
    Q_INVOKABLE void processAllPeerAssignments(
        const client::p2p::AllPeerRoleAssignments& allAssignments);

    // --- Getter methods ---
    // Returns a copy of all role definitions
    QMap<QString, client::p2p::RoleDefinition> getRoleDefinitions() const;
    // Gets a specific role definition by name
    client::p2p::RoleDefinition getRoleDefinition(const QString& roleName,
                                                  bool* ok = nullptr) const;

    // Method to set the local peer's ID once received from the server
    void setLocalPeerId(const QString& peerId);
    // To be used by GUI components
    QString getLocalPeerId() const;
    void storePeerUsername(const QString& peerId, const QString& username);
    QString getPeerUsername(const QString& peerId) const;
    // Gets assigned role names for a peer
    QVector<QString> getAssignedRoleNames(const QString& peerId) const;
    // Gets the HLC timestamp of a peer's last role assignment
    qint64 getPeerAssignmentHlc(const QString& peerId) const;

    // Calculates the effective permission mask for a given peer
    quint32 getPermissionsForPeer(const QString& peerId) const;

signals:
    void
    definitionsChanged(); // Emitted when the set of role definitions changes
    void peerRolesChanged(
        const QString& peerId); // Emitted when a specific peer's roles change
    void allAssignmentsRefreshed(); // Emitted after processing
                                    // AllPeerRoleAssignments
    void localPeerIdConfirmed(
        const QString& peerId); // Emitted when the local peer ID is set

private:
    // Internal storage
    // Key: role name (QString, normalized to lower for lookup consistency if
    // needed)
    QMap<QString, client::p2p::RoleDefinition> roleDefinitions;

    // Key: peer ID (QString)
    // Value: The PeerRoleAssignment protobuf message, which contains the list
    // of role names and its HLC
    QMap<QString, client::p2p::PeerRoleAssignment> peerAssignments;

    // Store the true local peer ID
    QString localPeerId;
    QMap<QString, QString> peerUsernames; // peerId -> username

    // Mutex for thread-safe access to the internal maps.
    // QReadWriteLock is good if reads are much more frequent than writes.
    // Use QMutex if write frequency is comparable or for simplicity.
    mutable QReadWriteLock storeLock;
};

} // namespace Roles
} // namespace P2P
