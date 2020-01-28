// Copyright 2019 Canonical Ltd.
// Licensed under the LGPLv3 with static-linking exception.
// See LICENCE file for details.

package tpm2

// Section 28 - Context Management

import (
	"bytes"
	"crypto"
	_ "crypto/sha256"
	"errors"
	"fmt"
	"reflect"
)

type objectContextData struct {
	Public    *Public `tpm2:"sized"`
	Name      Name
}

type sessionContextData struct {
	IsAudit        bool
	IsExclusive    bool
	HashAlg        HashAlgorithmId
	SessionType    SessionType
	PolicyHMACType policyHMACType
	IsBound        bool
	BoundEntity    Name
	SessionKey     Digest
	NonceCaller    Nonce
	NonceTPM       Nonce
	Symmetric      *SymDef
}

type resourceContextDataU struct {
	Data interface{}
}

func (d resourceContextDataU) Select(selector reflect.Value) (reflect.Type, error) {
	switch selector.Interface().(uint8) {
	case contextTypeObject:
		return reflect.TypeOf((*objectContextData)(nil)), nil
	case contextTypeSession:
		return reflect.TypeOf((*sessionContextData)(nil)), nil
	}
	return nil, invalidSelectorError{selector}
}

const (
	contextTypeObject uint8 = iota
	contextTypeSession
)

type resourceContextData struct {
	ContextType uint8
	Data        resourceContextDataU `tpm2:"selector:ContextType"`
	TPMBlob     ContextData
}

func wrapContextBlob(tpmBlob ContextData, context HandleContext) ContextData {
	d := resourceContextData{TPMBlob: tpmBlob}

	switch c := context.(type) {
	case *objectContext:
		d.ContextType = contextTypeObject
		d.Data.Data = &objectContextData{Public: &c.public, Name: c.name}
	case *sessionContext:
		d.ContextType = contextTypeSession
		d.Data.Data = &sessionContextData{
			IsAudit:        c.isAudit,
			IsExclusive:    c.isExclusive,
			HashAlg:        c.hashAlg,
			SessionType:    c.sessionType,
			PolicyHMACType: c.policyHMACType,
			IsBound:        c.isBound,
			BoundEntity:    c.boundEntity,
			SessionKey:     c.sessionKey,
			NonceCaller:    c.nonceCaller,
			NonceTPM:       c.nonceTPM,
			Symmetric:      c.symmetric}
	}

	data, err := MarshalToBytes(d)
	if err != nil {
		panic(fmt.Sprintf("cannot marshal wrapped resource context data: %v", err))
	}

	h := crypto.SHA256.New()
	h.Write(data)

	data, err = MarshalToBytes(HashAlgorithmSHA256, h.Sum(nil), data)
	if err != nil {
		panic(fmt.Sprintf("cannot marshal wrapped resource context data and checksum: %v", err))
	}

	return data
}

// ContextSave executes the TPM2_ContextSave command on the handle referenced by saveContext, in order to save the context associated
// with that handle outside of the TPM. The TPM encrypts and integrity protects the context with a key derived from the hierarchy
// proof. If saveContext does not correspond to a transient object or a session, then it will return an error.
//
// On successful completion, it returns a Context instance that can be passed to TPMContext.ContextLoad. Note that this function
// wraps the context data returned from the TPM with some host-side state associated with the resource, so that it can be restored
// fully in TPMContext.ContextLoad. If saveContext corresponds to a session, the host-side state that is added to the returned context
// blob includes the session key.
//
// If saveContext corresponds to a session, then TPM2_ContextSave also removes resources associated with the session from the TPM
// (it becomes a saved session rather than a loaded session). In this case, saveContext is marked as not loaded and can only be used
// as an argument to TPMContext.FlushContext until it is loaded again via TPMContext.ContextLoad.
//
// If saveContext corresponds to a session and no more contexts can be saved, a *TPMError error will be returned with an error code
// of ErrorTooManyContexts. If a context ID cannot be assigned for the session, a *TPMWarning error with a warning code of
// WarningContextGap will be returned.
func (t *TPMContext) ContextSave(saveContext HandleContext) (*Context, error) {
	if sc, isSession := saveContext.(*sessionContext); isSession && !sc.usable {
		return nil, makeInvalidParamError("saveContext", "unusable session HandleContext")
	}

	var context Context

	if err := t.RunCommand(CommandContextSave, nil,
		saveContext, Separator,
		Separator,
		Separator,
		&context); err != nil {
		return nil, err
	}

	context.Blob = wrapContextBlob(context.Blob, saveContext)

	if sc, isSession := saveContext.(*sessionContext); isSession {
		sc.usable = false
	}

	return &context, nil
}

// ContextLoad executes the TPM2_ContextLoad command with the supplied Context, in order to restore a context previously saved from
// TPMContext.ContextSave.
//
// If the size field of the integrity HMAC in the context blob is greater than the size of the largest digest algorithm, a *TPMError
// with an error code of ErrorSize is returned. If the context blob is shorter than the size indicated for the integrity HMAC, a
// *TPMError with an error code of ErrorInsufficient is returned.
//
// If the size of the context's integrity HMAC does not match the context integrity digest algorithm for the TPM, or the context
// blob is too short, a *TPMParameterError error with an error code of ErrorSize will be returned. If the integrity HMAC check fails,
// a *TPMParameterError with an error code of ErrorIntegrity will be returned.
//
// If the hierarchy that the context is part of is disabled, a *TPMParameterError error with an error code of ErrorHierarchy will be
// returned.
//
// If the context corresponds to a session but the handle doesn't reference a saved session or the sequence number is invalid, a
// *TPMParameterError error with an error code of ErrorHandle will be returned.
//
// If the context corresponds to a session and no more sessions can be created until the oldest session is context loaded, and context
// doesn't correspond to the oldest session, a *TPMWarning error with a warning code of WarningContextGap will be returned.
//
// If there are no more slots available for objects or loaded sessions, a *TPMWarning error with a warning code of either
// WarningSessionMemory or WarningObjectMemory will be returned.
//
// On successful completion, it returns a HandleContext which corresponds to the resource loaded in to the TPM. If the context
// corresponds to an object, this will always be a new ResourceContext. If context corresponds to a session, then the returned
// SessionContext will be newly created unless there is still an active SessionContext for that saved session, in which case the
// existing SessionContext will be marked as loaded and returned instead.
func (t *TPMContext) ContextLoad(context *Context) (HandleContext, error) {
	if context == nil {
		return nil, makeInvalidParamError("context", "nil value")
	}

	var integrityAlg HashAlgorithmId
	var integrity []byte
	var data []byte
	if _, err := UnmarshalFromBytes(context.Blob, &integrityAlg, &integrity, &data); err != nil {
		return nil, fmt.Errorf("cannot load context: cannot unpack checksum and data blob: %v", err)
	}

	if !integrityAlg.Supported() {
		return nil, errors.New("cannot load context: invalid checksum algorithm")
	}

	h := integrityAlg.NewHash()
	h.Write(data)
	if !bytes.Equal(h.Sum(nil), integrity) {
		return nil, errors.New("cannot load context: invalid checksum")
	}

	var d resourceContextData
	if _, err := UnmarshalFromBytes(data, &d); err != nil {
		return nil, fmt.Errorf("cannot load context: cannot unmarshal data blob: %v", err)
	}

	switch d.ContextType {
	case contextTypeObject:
		if context.SavedHandle.Type() != HandleTypeTransient {
			return nil, errors.New("cannot load context: inconsistent handle type")
		}
		dd := d.Data.Data.(*objectContextData)
		if !dd.Public.compareName(dd.Name) {
			return nil, errors.New("cannot load context for object: public area and name don't match")
		}
	case contextTypeSession:
		switch context.SavedHandle.Type() {
		case HandleTypeHMACSession, HandleTypePolicySession:
		default:
			return nil, errors.New("cannot load context: inconsistent handle type")
		}
		dd := d.Data.Data.(*sessionContextData)
		if !dd.IsAudit && dd.IsExclusive {
			return nil, fmt.Errorf("cannot load context for session: inconsistent audit attributes")
		}
		if !dd.HashAlg.Supported() {
			return nil, fmt.Errorf("cannot load context for session: invalid hash algorithm %v", dd.HashAlg)
		}
		switch dd.SessionType {
		case SessionTypeHMAC, SessionTypePolicy, SessionTypeTrial:
		default:
			return nil, fmt.Errorf("cannot load context for session: invalid type %v", dd.SessionType)
		}
		if dd.PolicyHMACType > policyHMACTypeMax {
			return nil, errors.New("cannot load context for session: invalid value")
		}
		if (dd.IsBound && len(dd.BoundEntity) == 0) || (!dd.IsBound && len(dd.BoundEntity) > 0) {
			return nil, errors.New("cannot load context for session: inconsistent properties")
		}
		digestSize := dd.HashAlg.Size()
		if len(dd.SessionKey) != digestSize && len(dd.SessionKey) != 0 {
			return nil, errors.New("cannot load context for session: unexpected session key size")
		}
		if len(dd.NonceCaller) != digestSize || len(dd.NonceTPM) != digestSize {
			return nil, errors.New("cannot load context for session: unexpected nonce size")
		}
		switch dd.Symmetric.Algorithm {
		case SymAlgorithmAES, SymAlgorithmXOR, SymAlgorithmNull, SymAlgorithmSM4, SymAlgorithmCamellia:
		default:
			return nil, fmt.Errorf("cannot load context for session: invalid symmetric algorithm %v", dd.Symmetric.Algorithm)
		}
		switch dd.Symmetric.Algorithm {
		case SymAlgorithmAES, SymAlgorithmSM4, SymAlgorithmCamellia:
			if dd.Symmetric.Mode.Sym() != SymModeCFB {
				return nil, fmt.Errorf("cannot load context for session: invalid symmetric mode %v", dd.Symmetric.Mode.Sym())
			}
		}
	default:
		return nil, errors.New("cannot load context: inconsistent attributes")
	}

	tpmContext := Context{
		Sequence:    context.Sequence,
		SavedHandle: context.SavedHandle,
		Hierarchy:   context.Hierarchy,
		Blob:        d.TPMBlob}

	var loadedHandle Handle

	if err := t.RunCommand(CommandContextLoad, nil,
		Separator,
		tpmContext, Separator,
		&loadedHandle); err != nil {
		return nil, err
	}

	var rc HandleContext

	switch d.ContextType {
	case contextTypeObject:
		if loadedHandle.Type() != HandleTypeTransient {
			return nil, &InvalidResponseError{CommandContextLoad,
				fmt.Sprintf("handle 0x%08x returned from TPM is the wrong type", loadedHandle)}
		}

		dd := d.Data.Data.(*objectContextData)
		rc = &objectContext{handle: loadedHandle, public: Public(*dd.Public), name: dd.Name}
	case contextTypeSession:
		if loadedHandle != context.SavedHandle {
			return nil, &InvalidResponseError{CommandContextLoad, fmt.Sprintf("handle 0x%08x returned from TPM is incorrect", loadedHandle)}
		}
		var sc *sessionContext
		if rc, exists := t.resources[normalizeHandleForMap(loadedHandle)]; exists {
			sc = rc.(*sessionContext)
		} else {
			sc = &sessionContext{handle: loadedHandle}
		}

		dd := d.Data.Data.(*sessionContextData)
		sc.handle = loadedHandle
		sc.usable = true
		sc.isAudit = dd.IsAudit
		sc.isExclusive = dd.IsExclusive && sc == t.exclusiveSession
		sc.hashAlg = dd.HashAlg
		sc.sessionType = dd.SessionType
		sc.policyHMACType = dd.PolicyHMACType
		sc.isBound = dd.IsBound
		sc.boundEntity = dd.BoundEntity
		sc.sessionKey = dd.SessionKey
		sc.nonceCaller = dd.NonceCaller
		sc.nonceTPM = dd.NonceTPM
		sc.symmetric = dd.Symmetric

		rc = sc
	}
	t.addHandleContext(rc)

	return rc, nil
}

// FlushContext executes the TPM2_FlushContext command on the handle referenced by flushContext, in order to flush resources
// associated with it from the TPM. If flushContext does not correspond to a transient object or a session, then it will return
// with an error.
//
// On successful completion, flushContext is invalidated. If flushContext corresponded to a session, then it will no longer be
// possible to restore that session with TPMContext.ContextLoad, even if it was previously saved with TPMContext.ContextSave.
func (t *TPMContext) FlushContext(flushContext HandleContext) error {
	if err := t.checkHandleContextParam(flushContext); err != nil {
		return makeInvalidParamError("flushContext", fmt.Sprintf("%v", err))
	}

	if err := t.RunCommand(CommandFlushContext, nil,
		Separator,
		flushContext.Handle()); err != nil {
		return err
	}

	t.evictHandleContext(flushContext)
	return nil
}

// EvictControl executes the TPM2_EvictControl command on the handle referenced by object. To persist a transient object,
// object should correspond to the transient object and persistentHandle should specify the persistent handle to which the
// resource associated with object should be persisted. To evict a persistent object, object should correspond to the
// persistent object and persistentHandle should be the handle associated with that resource.
//
// The auth parameter should be a ResourceContext that corresponds to a hierarchy - it should be HandlePlatform for objects within
// the platform hierarchy, or HandleOwner for objects within the storage or endorsement hierarchies. If auth is a ResourceContext
// corresponding to HandlePlatform but object corresponds to an object outside of the platform hierarchy, or auth is a ResourceContext
// corresponding to HandleOwner but object corresponds to an object inside of the platform hierarchy, a *TPMHandleError error with
// an error code of ErrorHierarchy will be returned for handle index 2. The auth handle requires authorization with the user auth
// role, with session based authorization provided via authAuthSession.
//
// If object corresponds to a transient object that only has a public part loaded, or which has the AttrStClear attribute set,
// then a *TPMHandleError error with an error code of ErrorAttributes will be returned for handle index 2.
//
// If object corresponds to a persistent object and persistentHandle is not the handle for that object, a *TPMHandleError error
// with an error code of ErrorHandle will be returned for handle index 2.
//
// If object corresponds to a transient object and persistentHandle is not in the correct range determined by the value of
// auth, a *TPMParameterError error with an error code of ErrorRange will be returned.
//
// If there is insuffient space to persist a transient object, a *TPMError error with an error code of ErrorNVSpace will be returned.
// If a persistent object already exists at the specified handle, a *TPMError error with an error code of ErrorNVDefined will be
// returned.
//
// On successful completion of persisting a transient object, it returns a ResourceContext that corresponds to the persistent object.
// On successful completion of evicting a persistent object, it returns a nil ResourceContext, and object will be invalidated.
func (t *TPMContext) EvictControl(auth, object ResourceContext, persistentHandle Handle, authAuthSession *Session, sessions ...*Session) (ResourceContext, error) {
	if err := t.RunCommand(CommandEvictControl, sessions,
		ResourceContextWithSession{Context: auth, Session: authAuthSession}, object, Separator,
		persistentHandle); err != nil {
		return nil, err
	}

	if object.Handle() == persistentHandle {
		t.evictHandleContext(object)
		return nil, nil
	}

	public := &object.(*objectContext).public
	objectContext := &objectContext{handle: persistentHandle, name: object.Name()}
	public.copyTo(&objectContext.public)
	t.addHandleContext(objectContext)

	return objectContext, nil
}
